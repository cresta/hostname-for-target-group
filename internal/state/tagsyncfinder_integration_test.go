// +build integration

package state_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"
	"github.com/cresta/hostname-for-target-group/internal/state"
	"github.com/stretchr/testify/require"
)

func TestTagSyncFinder(t *testing.T) {
	if os.Getenv("TAG_KEY") == "" || os.Getenv("ARN_PAIRS") == "" {
		t.Skip("skipping test: expect env TAG_KEY=<tag_key> and ARN_PAIRS=<arn>=<hostname>,<arn>=<hostname>...")
	}
	expectedMap := make(map[state.TargetGroupARN]string)
	for _, part := range strings.Split(os.Getenv("ARN_PAIRS"), ",") {
		p2 := strings.SplitN(part, "=", 2)
		expectedMap[state.TargetGroupARN(p2[0])] = p2[1]
	}
	ses, err := session.NewSession()
	require.NoError(t, err)
	client := resourcegroupstaggingapi.New(ses)
	tf := state.TagSyncFinder{
		Client: client,
		TagKey: os.Getenv("TAG_KEY"),
	}

	m, err := tf.ToSync(context.Background())
	require.NoError(t, err)
	require.Len(t, m, len(expectedMap))
	for k, v := range m {
		require.Equal(t, v, expectedMap[k])
	}
}
