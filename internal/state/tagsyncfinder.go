package state

import (
	"context"
	"fmt"
	"time"

	"github.com/cresta/zapctx"
	"go.uber.org/zap"

	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
)

type TagSyncFinder struct {
	Client *resourcegroupstaggingapi.ResourceGroupsTaggingAPI
	TagKey string
	Log    *zapctx.Logger
}

func (t *TagSyncFinder) ToSync(ctx context.Context) (map[TargetGroupARN]string, error) {
	t.Log.Debug(ctx, "<- ToSync")
	defer t.Log.Debug(ctx, "-> ToSync")
	res := make(map[TargetGroupARN]string, 10)
	err := t.Client.GetResourcesPagesWithContext(ctx, &resourcegroupstaggingapi.GetResourcesInput{
		TagFilters: []*resourcegroupstaggingapi.TagFilter{
			{
				Key: &t.TagKey,
			},
		},
	}, func(output *resourcegroupstaggingapi.GetResourcesOutput, b bool) bool {
	outer:
		for _, m := range output.ResourceTagMappingList {
			for _, tag := range m.Tags {
				if *tag.Key == t.TagKey {
					res[TargetGroupARN(*m.ResourceARN)] = *tag.Value
					continue outer
				}
			}
		}
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("uanble to page results: %w", err)
	}
	t.Log.Debug(ctx, "found arn to sync", zap.Any("arn", res))
	return res, nil
}

var _ SyncFinder = &TagSyncFinder{}

type CachedSyncer struct {
	SyncFinder    SyncFinder
	SyncCache     SyncCache
	Log           *zapctx.Logger
	CacheDuration time.Duration
}

func (c *CachedSyncer) ToSync(ctx context.Context) (map[TargetGroupARN]string, error) {
	now := time.Now()
	cachedRes, err := c.SyncCache.GetSync(ctx, now)
	if err != nil {
		c.Log.IfErr(err).Warn(ctx, "unable to fetch cached sync state")
	} else if cachedRes != nil {
		c.Log.Debug(ctx, "found state in cache")
		return cachedRes, nil
	}
	newRes, err := c.SyncFinder.ToSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to find arn to sync: %w", err)
	}
	if err := c.SyncCache.StoreSync(ctx, newRes, now.Add(c.CacheDuration)); err != nil {
		c.Log.IfErr(err).Warn(ctx, "unable to store state into cache")
	} else {
		c.Log.Debug(ctx, "stored state in cache")
	}

	return newRes, nil
}

var _ SyncFinder = &CachedSyncer{}
