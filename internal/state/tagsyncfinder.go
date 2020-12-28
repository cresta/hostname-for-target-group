package state

import (
	"context"
	"fmt"

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
