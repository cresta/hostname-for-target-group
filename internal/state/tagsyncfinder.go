package state

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
)

type TagSyncFinder struct {
	Client *resourcegroupstaggingapi.ResourceGroupsTaggingAPI
	TagKey string
}

func (t *TagSyncFinder) ToSync(ctx context.Context) (map[TargetGroupARN]string, error) {
	res := make(map[TargetGroupARN]string, 10)
	err := t.Client.GetResourcesPagesWithContext(ctx, &resourcegroupstaggingapi.GetResourcesInput{
		TagFilters:                []*resourcegroupstaggingapi.TagFilter{
			{
				Key:    &t.TagKey,
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
	return res, nil
}

var _ SyncFinder = &TagSyncFinder{}