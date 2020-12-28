package syncer

import (
	"context"
	"fmt"
	"math/rand"
	"net"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/aws/aws-sdk-go/service/elbv2/elbv2iface"
	"github.com/cresta/hostname-for-target-group/internal/state"
	"github.com/cresta/zapctx"
	"go.uber.org/zap"
)

type Config struct {
	InvocationsBeforeDeregistration int
	RemoveUnknownTgIp               bool
}

type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

var _ Resolver = &net.Resolver{}

type MultiResolver struct {
	coreResolvers []*net.Resolver
	logger        *zapctx.Logger
}

func NewMultiResolver(logger *zapctx.Logger, dnsServers []string) *MultiResolver {
	var ret MultiResolver
	ret.logger = logger
	for _, dnsServer := range dnsServers {
		// Setting dnsServer is important b/c of how for work in go
		dnsServer := dnsServer
		ret.coreResolvers = append(ret.coreResolvers, &net.Resolver{
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				// Dial with the same network stack, but to 'dnsServer'
				return net.Dial(network, dnsServer)
			},
		})
	}
	return &ret
}

func (m *MultiResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	logger := m.logger.With(zap.String("host", host))
	logger.Debug(ctx, "starting lookup")
	idx := rand.Intn(len(m.coreResolvers))
	var lastErr error
	for i := 0; i < len(m.coreResolvers); i++ {
		resolverIdx := (idx + i) % len(m.coreResolvers)
		var ip []net.IPAddr
		ip, lastErr = m.coreResolvers[resolverIdx].LookupIPAddr(ctx, host)
		logger.IfErr(lastErr).Warn(ctx, "unable to look up host", zap.Int("resolver_index", resolverIdx))
		if lastErr == nil {
			return ip, nil
		}
	}
	logger.IfErr(lastErr).Warn(ctx, "unable to find any resolution for host")
	return nil, lastErr
}

var _ Resolver = &MultiResolver{}

type Syncer struct {
	Log        *zapctx.Logger
	State      state.Storage
	Client     elbv2iface.ELBV2API
	Config     Config
	Resolver   Resolver
	SyncFinder state.SyncFinder
}

func (s *Syncer) Sync(ctx context.Context) error {
	toSyncMap, err := s.SyncFinder.ToSync(ctx)
	if err != nil {
		return fmt.Errorf("unable to get tg to sync: %w", err)
	}
	currentStates, err := s.State.GetStates(ctx, getSyncKeys(toSyncMap))
	if err != nil {
		return fmt.Errorf("unable to get any states: %w", err)
	}
	allResults := make(map[state.Keys]state.State, len(toSyncMap))
	for tgArn, hostname := range toSyncMap {
		singleResult, err := s.syncSingle(ctx, tgArn, hostname, currentStates[state.Keys{
			TargetGroupARN: tgArn,
			Hostname:       hostname,
		}])
		if err != nil {
			s.Log.Warn(ctx, "unable to run sync", zap.String("tg", string(tgArn)), zap.String("hostname", hostname))
		} else {
			allResults[state.Keys{
				TargetGroupARN: tgArn,
				Hostname:       hostname,
			}] = *singleResult
		}
	}
	err = s.State.Store(ctx, allResults)
	if err != nil {
		return fmt.Errorf("unable to store final results: %w", err)
	}
	return nil
}

func getSyncKeys(syncMap map[state.TargetGroupARN]string) []state.Keys {
	ret := make([]state.Keys, 0, len(syncMap))
	for k, v := range syncMap {
		ret = append(ret, state.Keys{
			TargetGroupARN: k,
			Hostname:       v,
		})
	}
	return ret
}

func targetsToTimesMissed(t []state.Target) map[string]int {
	ret := make(map[string]int, len(t))
	for i := range t {
		ret[t[i].IP] = t[i].TimesMissing
	}
	return ret
}

func (s *Syncer) resolveIPs(ctx context.Context, hostname string) ([]string, error) {
	addrs, err := s.Resolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve IP for %s: %w", hostname, err)
	}
	// Fetch all the IPv4 addresses
	allIPs := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		asIPv4 := addr.IP.To4()
		if asIPv4.IsUnspecified() {
			continue
		}
		allIPs = append(allIPs, asIPv4.String())
	}
	return allIPs, nil
}

func (s *Syncer) getTargetGroupIPs(ctx context.Context, targetGroupARN state.TargetGroupARN) ([]string, error) {
	out, err := s.Client.DescribeTargetHealthWithContext(ctx, &elbv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(string(targetGroupARN)),
	})
	if err != nil {
		return nil, fmt.Errorf("unable to describe target group %s: %w", targetGroupARN, err)
	}
	ret := make([]string, 0, len(out.TargetHealthDescriptions))
	for _, target := range out.TargetHealthDescriptions {
		ret = append(ret, *target.Target.Id)
	}
	return ret, nil
}

func (s *Syncer) syncSingle(ctx context.Context, targetGroupARN state.TargetGroupARN, hostname string, previousResult state.State) (*state.State, error) {
	allIPs, err := s.resolveIPs(ctx, hostname)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve IP for %s: %w", hostname, err)
	}
	currentlyStoredIPs, err := s.getTargetGroupIPs(ctx, targetGroupARN)
	if err != nil {
		return nil, fmt.Errorf("unable to get target group IPs %s: %w", targetGroupARN, err)
	}

	ipToRemove, ipToAdd, newState := resolve(previousResult, currentlyStoredIPs, allIPs, s.Config.InvocationsBeforeDeregistration, s.Config.RemoveUnknownTgIp)
	if len(ipToAdd) > 0 {
		_, err = s.Client.RegisterTargetsWithContext(ctx, &elbv2.RegisterTargetsInput{
			TargetGroupArn: aws.String(string(targetGroupARN)),
			Targets:        createTargets(ipToAdd),
		})
		if err != nil {
			s.Log.IfErr(err).Warn(ctx, "unable to register targets", zap.Strings("targets", ipToAdd))
			return nil, fmt.Errorf("unable to register targets with %s: %w", targetGroupARN, err)
		}
	}
	if len(ipToRemove) > 0 {
		_, err = s.Client.DeregisterTargetsWithContext(ctx, &elbv2.DeregisterTargetsInput{
			TargetGroupArn: aws.String(string(targetGroupARN)),
			Targets:        createTargets(ipToRemove),
		})
		if err != nil {
			s.Log.IfErr(err).Warn(ctx, "unable to deregister targets", zap.Strings("targets", ipToRemove))
			return nil, fmt.Errorf("unable to deregister targets with %s: %w", targetGroupARN, err)
		}
	}
	return &newState, nil
}

func createNewState(tm map[string]int) state.State {
	var ret state.State
	for ip, misses := range tm {
		ret.Targets = append(ret.Targets, state.Target{
			IP:           ip,
			TimesMissing: misses,
		})
	}
	return ret
}

func createTargets(add []string) []*elbv2.TargetDescription {
	ret := make([]*elbv2.TargetDescription, len(add))
	for i := range add {
		ret[i] = &elbv2.TargetDescription{
			Id: aws.String(add[i]),
		}
	}
	return ret
}

func stringSetDiff(oldStrings []string, newStrings []string) (toAdd []string, toRemove []string) {
	stringsToRemove := make([]string, 0, len(oldStrings))
	stringsToAdd := make([]string, 0, len(newStrings))
	oldSet := listToSet(oldStrings)
	newSet := listToSet(newStrings)
	for v := range oldSet {
		// in old, not in new == remove
		if _, exists := newSet[v]; !exists {
			stringsToRemove = append(stringsToRemove, v)
		}
	}
	for v := range newSet {
		// in new, not in old == add
		if _, exists := oldSet[v]; !exists {
			stringsToAdd = append(stringsToAdd, v)
		}
	}
	return stringsToAdd, stringsToRemove
}

func listToSet(m []string) map[string]struct{} {
	ret := make(map[string]struct{}, len(m))
	for _, s := range m {
		ret[s] = struct{}{}
	}
	return ret
}

func resolve(previousResult state.State, currentlyStoredIPs []string, resolvedIPs []string, invocationsBeforeDeregistration int, removeUnknownIPs bool) (ipToRemove []string, ipToAdd []string, stateToStore state.State) {
	// To turn previous results into resolved IPs, calculate which IP to add and which to remove
	toAdd, currentlyMissing := stringSetDiff(currentlyStoredIPs, resolvedIPs)
	// Increment the "times missed" for each missed IP
	tm := targetsToTimesMissed(previousResult.Targets)
	for _, tr := range currentlyMissing {
		if _, exists := tm[tr]; exists {
			tm[tr]++
		} else if removeUnknownIPs {
			tm[tr] = invocationsBeforeDeregistration + 1
		}
	}
	// Reset to zero any target I'm now seeing
	for _, ip := range resolvedIPs {
		tm[ip] = 0
	}

	// Remove everything that is over the deregistration limit
	removeTargets := make([]string, 0, len(tm))
	for k, v := range tm {
		if v >= invocationsBeforeDeregistration && v > 0 {
			removeTargets = append(removeTargets, k)
		}
	}

	// Remove from previousResult.Targets any things we deregistered
	for _, k := range removeTargets {
		delete(tm, k)
	}

	newState := createNewState(tm)
	newState.Version = previousResult.Version + 1
	return removeTargets, toAdd, newState
}
