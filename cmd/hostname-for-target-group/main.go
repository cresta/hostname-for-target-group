package main

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/cresta/gotracing"
	"github.com/cresta/gotracing/datadog"
	"github.com/cresta/hostname-for-target-group/internal/state"
	"github.com/cresta/hostname-for-target-group/internal/syncer"
	"github.com/cresta/zapctx"
	"github.com/signalfx/golib/v3/errors"
	"github.com/signalfx/golib/v3/httpdebug"
	"go.uber.org/zap"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type config struct {
	ListenAddr                      string
	DebugListenAddr                 string
	Tracer                          string
	DynamoDBTable                   string
	TgFromTagKey                    string
	DNSServers                      string
	InvocationsBeforeDeregistration string
	RemoveUnknownTgIp               string
	DaemonMode                      string
	DnsRefreshInterval              string
	TagSearchInterval               string
	ElbTgArn                        string
	TargetFqdn                      string
	LambdaMode                      string
	TagCachePrefix                  string
}

func (c config) WithDefaults() config {
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	if c.DebugListenAddr == "" {
		c.DebugListenAddr = ":6060"
	}
	if c.DnsRefreshInterval == "" {
		c.DnsRefreshInterval = "5s"
	}
	if c.TagSearchInterval == "" {
		c.TagSearchInterval = "30s"
	}
	if c.InvocationsBeforeDeregistration == "" {
		c.InvocationsBeforeDeregistration = "3"
	}
	return c
}

func getConfig() config {
	return config{
		// Defaults to ":8080"
		ListenAddr:    os.Getenv("LISTEN_ADDR"),
		// Defaults to ":6060"
		DebugListenAddr: os.Getenv("DEBUG_ADDR"),
		// Allows you to use a dynamic tracer
		Tracer:          os.Getenv("TRACER"),
		// Which dynamodb table to write/read sync results from/to
		DynamoDBTable:   os.Getenv("DYNAMODB_TABLE"),
		// The target group to monitor.  Overridden by TG_FROM_TAG_KEY
		ElbTgArn: os.Getenv("ELB_TG_ARN"),
		// The host to resolve ElbTgArn into.  Overridden by TG_FROM_TAG_KEY
		TargetFqdn: os.Getenv("TARGET_FQDN"),
		// If set, will search for Target groups with this tag key and sync the IPs of that target group
		TgFromTagKey:    os.Getenv("TG_FROM_TAG_KEY"),
		// Comma separated list of DNS servers to query
		DNSServers:       os.Getenv("DNS_SERVERS"),
		// If set, will require this many invocations before deregistring an IP
		InvocationsBeforeDeregistration: os.Getenv("INVOCATIONS_BEFORE_DEREGISTRATION"),
		// If true, will also remove IPs from the target group that never had a state
		RemoveUnknownTgIp: os.Getenv("REMOVE_UNKNOWN_TG_IP"),
		// If true, will run the service continuously, sleeping DNS_REFRESH_INTERVAL
		DaemonMode: os.Getenv("DAEMON_MODE"),
		// When in daemon mode, will sleep this long between refreshes
		DnsRefreshInterval: os.Getenv("DNS_REFRESH_INTERVAL"),
		// If using mode TG_FROM_TAG_KEY, the interval between searching for tags
		// This can be useful since the tags change very infrequently
		TagSearchInterval: os.Getenv("TAG_SEARCH_INTERVAL"),
		// If true, will run as a lambda handler.  Overwrites DAEMON_MODE setting and ignores DNS_REFRESH_INTERVAL
		LambdaMode: os.Getenv("LAMBDA_MODE"),
		// Optional: Adds a prefix key to fetches for tag cache.
		TagCachePrefix: os.Getenv("TAG_CACHE_PREFIX"),
	}.WithDefaults()
}

func main() {
	instance.Main()
}

type Service struct {
	osExit   func(int)
	config   config
	log      *zapctx.Logger
	onListen func(net.Listener)
	server   *http.Server
	tracers  *gotracing.Registry
	stateStorage state.Storage
	syncFinder state.SyncFinder
	syncCache state.SyncCache
	resolver syncer.Resolver
	syncer *syncer.Syncer
}

var instance = Service{
	osExit: os.Exit,
	config: getConfig(),
	tracers: &gotracing.Registry{
		Constructors: map[string]gotracing.Constructor{
			"datadog": datadog.NewTracer,
		},
	},
}

func setupLogging() (*zapctx.Logger, error) {
	l, err := zap.NewProduction()
	if err != nil {
		return nil, err
	}
	return zapctx.New(l), nil
}

type runningMode int
const (
	lambda runningMode = iota
	daemon
	oneTime
)

func (m *Service) getRunningMode() runningMode {
	isLambdaMode, err := strconv.ParseBool(m.config.LambdaMode)
	if err == nil && isLambdaMode {
		return lambda
	}
	isDaemonMode, err := strconv.ParseBool(m.config.DaemonMode)
	if err == nil && isDaemonMode {
		return daemon
	}
	return oneTime
}

func (m *Service) Main() {
	cfg := m.config
	if m.log == nil {
		var err error
		m.log, err = setupLogging()
		if err != nil {
			fmt.Printf("Unable to setup logging: %v", err)
			m.osExit(1)
			return
		}
	}
	m.log.Info(context.Background(), "Starting")
	rootTracer, err := m.tracers.New(m.config.Tracer, gotracing.Config{
		Log: m.log.With(zap.String("section", "setup_tracing")),
		Env: os.Environ(),
	})
	if err != nil {
		m.log.IfErr(err).Error(context.Background(), "unable to setup tracing")
		m.osExit(1)
		return
	}

	ctx := context.Background()
	m.log = m.log.DynamicFields(rootTracer.DynamicFields()...)
	if err := m.injection(ctx); err != nil {
		m.log.IfErr(err).Panic(ctx, "unable to inject starting variables")
		m.osExit(1)
		return
	}

	m.server = setupServer(cfg, m.log, rootTracer)
	shutdownCallback, err := setupDebugServer(m.log, cfg.DebugListenAddr, m)
	if err != nil {
		m.log.IfErr(err).Panic(context.Background(), "unable to setup debug server")
		m.osExit(1)
		return
	}

	ln, err := net.Listen("tcp", m.server.Addr)
	if err != nil {
		m.log.Panic(context.Background(), "unable to listen to port", zap.Error(err), zap.String("addr", m.server.Addr))
		m.osExit(1)
		return
	}
	if m.onListen != nil {
		m.onListen(ln)
	}

	serveErr := m.server.Serve(ln)
	if serveErr != http.ErrServerClosed {
		m.log.IfErr(serveErr).Error(context.Background(), "server existed")
	}
	m.log.Info(context.Background(), "Server finished")
	shutdownCallback()
	if serveErr != nil {
		m.osExit(1)
	}
}

func (m *Service) injection(ctx context.Context) error {
	var err error
	m.stateStorage, err = m.makeStateStorage(ctx)
	if err != nil {
		return fmt.Errorf("unable to make state storage: %w", err)
	}
	m.syncCache, err = m.makeSyncCache(ctx)
	if err != nil {
		return fmt.Errorf("unable to make sync finder: %w", err)
	}
	m.syncFinder, err = m.makeSyncFinder(ctx)
	if err != nil {
		return fmt.Errorf("unable to make sync finder: %w", err)
	}
	m.resolver, err = m.makeResolver(ctx)
	if err != nil {
		return fmt.Errorf("unable to make resolver: %w", err)
	}
	m.syncer = &syncer.Syncer{
		Log:        m.log.With("),
		State:      nil,
		Client:     nil,
		Config:     syncer.Config{},
		Resolver:   nil,
		SyncFinder: nil,
	}
	return nil
}

func (m *Service) makeStateStorage(ctx context.Context) (state.Storage, error) {
	if m.config.DynamoDBTable == "" {
		return nil, errors.New("expected env variable DYNAMODB_TABLE")
	}
	ses, err := session.NewSession()
	if err != nil {
		return nil, fmt.Errorf("unable to make aws session: %w", err)
	}
	logToUse := m.log.With(zap.String("class", "DynamoDBStorage"), zap.String("table_name", m.config.DynamoDBTable))
	logToUse.Debug(ctx, "using dynamodb cache")
	return &state.DynamoDBStorage{
		TableName:       m.config.DynamoDBTable,
		Log:             logToUse,
		Client:          dynamodb.New(ses),
		SyncCachePrefix: m.config.TagCachePrefix,
	}, nil
}

func (m *Service) makeResolver(ctx context.Context) (syncer.Resolver, error) {
	servers := strings.Split(m.config.DNSServers, ",")
	resolverLog := m.log.With(zap.String("servers", m.config.DNSServers))
	resolverLog.Debug(ctx, "using multi DNS resolver")
	return syncer.NewMultiResolver(resolverLog, servers), nil
}

func (m *Service) makeSyncCache(ctx context.Context) (state.SyncCache, error) {
	if m.getRunningMode() == daemon {
		m.log.Debug(ctx, "using local sync cache b/c of daemon mode")
		return &state.LocalSyncCache{}, nil
	}
	if asSyncCache, ok := m.stateStorage.(state.SyncCache); ok {
		m.log.Debug(ctx, "using remove storage as sync cache")
		return asSyncCache, nil
	}
	m.log.Warn(ctx, "falling back to local sync cache")
	return &state.LocalSyncCache{}, nil
}

func (m *Service) makeSyncFinder(ctx context.Context) (state.SyncFinder, error) {
	if m.config.TgFromTagKey == "" {
		if m.config.ElbTgArn == "" {
			return nil, fmt.Errorf("expect ELB_TG_ARN or TG_FROM_TAG_KEY set")
		}
		if m.config.TargetFqdn == "" {
			return nil, fmt.Errorf("expect TARGET_FQDN or TG_FROM_TAG_KEY set")
		}
		m.log.Debug(ctx, "making hard coded sync finder")
		return &state.HardCodedSyncFinder{
			TargetGroupARN: state.TargetGroupARN(m.config.ElbTgArn),
			Hostname:       m.config.TargetFqdn,
		}, nil
	}
	ses, err := session.NewSession()
	if err != nil {
		return nil, fmt.Errorf("unable to make aws session: %w", err)
	}
	syncFinderLogger := m.log.With(zap.String("tag_key", m.config.TgFromTagKey))
	syncFinderLogger.Debug(ctx, "using tag searching sync finder")
	return &state.TagSyncFinder{
		Client: resourcegroupstaggingapi.New(ses),
		TagKey: m.config.TgFromTagKey,
	}, nil
}

func setupDebugServer(l *zapctx.Logger, listenAddr string, obj interface{}) (func(), error) {
	if listenAddr == "" || listenAddr == "-" {
		return func() {
		}, nil
	}
	ret := httpdebug.New(&httpdebug.Config{
		Logger:        &zapctx.FieldLogger{Logger: l},
		ExplorableObj: obj,
	})
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("unable to listen to %s: %w", listenAddr, err)
	}
	go func() {
		serveErr := ret.Server.Serve(ln)
		if serveErr != http.ErrServerClosed {
			l.IfErr(serveErr).Error(context.Background(), "debug server existed")
		}
		l.Info(context.Background(), "debug server finished")
	}()
	return func() {
		err := ln.Close()
		l.IfErr(err).Warn(context.Background(), "unable to close listening socket for debug server")
	}, nil
}

