// +build integration

package state_test

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/cresta/hostname-for-target-group/internal/state"
	"github.com/cresta/zapctx/testhelp/testhelp"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func TestDynamoDBStorage(t *testing.T) {
	if os.Getenv("DYNAMODB_TABLE") == "" {
		t.Skip("skipping test: expect env DYNAMODB_TABLE=<dynamo_table>")
	}
	ses, err := session.NewSession()
	require.NoError(t, err)
	st := &state.DynamoDBStorage{
		TableName: os.Getenv("DYNAMODB_TABLE"),
		Log: testhelp.ZapTestingLogger(t),
		Client: dynamodb.New(ses),
	}
	testAnyStateStorage(t, st)
	testAnyStateCache(t, st)
}

func testAnyStateCache(t *testing.T, store state.SyncCache) {
	ctx := context.Background()
	prev, err := store.GetSync(ctx, time.Now())
	require.NoError(t, err)
	require.Nil(t, prev)
	now := time.Now()
	toCache := map[state.TargetGroupARN]string{
		"arn:test": "1.2.3.4",
	}
	err = store.StoreSync(ctx, toCache, now.Add(time.Minute))
	require.NoError(t, err)
	prev, err = store.GetSync(ctx, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, toCache, prev)

	prev, err = store.GetSync(ctx, now.Add(time.Second * 61))
	require.NoError(t, err)
	require.Nil(t, prev)
	err = store.StoreSync(ctx, nil, now.Add(time.Minute))
	require.NoError(t, err)
}

func testAnyStateStorage(t *testing.T, store state.Storage) {
	ctx := context.Background()
	testName := fmt.Sprintf("TestDynamoDBStorage:%s", time.Now())
	sk := state.StateKeys{
		TargetGroupARN: state.TargetGroupARN(testName),
		Hostname: "www.google.com",
	}
	// States should start missing
	out, err := store.GetStates(ctx, []state.StateKeys{sk})
	require.NoError(t, err)
	require.Empty(t, out[sk])
	storedStates := []state.Target{
		{
			IP:           "1.2.3.4",
			TimesMissing: 0,
		},{
			IP:           "1.2.3.5",
			TimesMissing: 3,
		},
	}
	// Should be able to add a state
	err = store.Store(ctx, map[state.StateKeys]state.State {
		sk: {
			Targets: storedStates,
			Version: 1,
		},
	})
	require.NoError(t, err)

	// Should see the state when you fetch it out
	out, err = store.GetStates(ctx, []state.StateKeys{sk})
	require.NoError(t, err)
	require.Len(t, out, 1)

	require.NotEmpty(t, out[sk])
	require.Equal(t, 1, out[sk].Version)
	require.Len(t, out[sk].Targets, 2)
	require.Equal(t, storedStates, out[sk].Targets)

	// Now remove the item
	err = store.Store(ctx, map[state.StateKeys]state.State {
		sk: {},
	})
	require.NoError(t, err)

	// And expect it gone
	out, err = store.GetStates(ctx, []state.StateKeys{sk})
	require.NoError(t, err)
	require.Empty(t, out[sk])
}