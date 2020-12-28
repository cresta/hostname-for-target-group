package state

import (
	"context"
	"sync"
	"time"
)

type TargetGroupARN string

type Storage interface {
	// GetStates returns a state result for each state key
	GetStates(ctx context.Context, syncPairs []Keys) (map[Keys]State, error)
	// Store results for all the state keys
	Store(ctx context.Context, toStore map[Keys]State) error
}

type Keys struct {
	TargetGroupARN TargetGroupARN
	Hostname       string
}

func (k Keys) String() string {
	return string(k.TargetGroupARN) + " " + k.Hostname
}

type SyncFinder interface {
	// Get the list of target groups -> hostname we should sync
	ToSync(ctx context.Context) (map[TargetGroupARN]string, error)
}

type HardCodedSyncFinder struct {
	TargetGroupARN TargetGroupARN
	Hostname       string
}

func (h *HardCodedSyncFinder) ToSync(_ context.Context) (map[TargetGroupARN]string, error) {
	return map[TargetGroupARN]string{
		h.TargetGroupARN: h.Hostname,
	}, nil
}

var _ SyncFinder = &HardCodedSyncFinder{}

type SyncCache interface {
	StoreSync(ctx context.Context, toStore map[TargetGroupARN]string, expireAt time.Time) error
	GetSync(ctx context.Context, currentTime time.Time) (map[TargetGroupARN]string, error)
}

type LocalSyncCache struct {
	store    map[TargetGroupARN]string
	expireAt time.Time
	mu       sync.Mutex
}

func (l *LocalSyncCache) copyStore() map[TargetGroupARN]string {
	ret := make(map[TargetGroupARN]string, len(l.store))
	for k, v := range l.store {
		ret[k] = v
	}
	return ret
}

func (l *LocalSyncCache) StoreSync(_ context.Context, toStore map[TargetGroupARN]string, expireAt time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = toStore
	l.store = l.copyStore() // Don't use the passed in pointer
	l.expireAt = expireAt
	return nil
}

func (l *LocalSyncCache) GetSync(_ context.Context, currentTime time.Time) (map[TargetGroupARN]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if currentTime.After(l.expireAt) {
		return nil, nil
	}
	return l.copyStore(), nil
}

var _ SyncCache = &LocalSyncCache{}

type Target struct {
	IP           string
	TimesMissing int
}

type State struct {
	Targets []Target
	Version int
}
