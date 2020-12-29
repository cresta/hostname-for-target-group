package syncer

import (
	"context"
	"testing"

	"github.com/cresta/hostname-for-target-group/internal/state"
	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	type testParams struct {
		name                            string
		previousResult                  state.State
		currentlyStoredIPs              []string
		resolvedIPs                     []string
		invocationsBeforeDeregistration int
		removeUnknownIPs                bool
		toRemove                        []string
		toAdd                           []string
		newState                        state.State
	}
	runs := []testParams{
		{
			name:        "first_run",
			resolvedIPs: []string{"1.2.3.4"},
			toAdd:       []string{"1.2.3.4"},
			toRemove:    []string{},
			newState: state.State{
				Targets: []state.Target{
					{
						IP:           "1.2.3.4",
						TimesMissing: 0,
					},
				},
				Version: 1,
			},
		}, {
			name:               "no_change",
			currentlyStoredIPs: []string{"1.2.3.4"},
			resolvedIPs:        []string{"1.2.3.4"},
			toAdd:              []string{},
			toRemove:           []string{},
			newState: state.State{
				Targets: []state.Target{
					{
						IP:           "1.2.3.4",
						TimesMissing: 0,
					},
				},
				Version: 1,
			},
		}, {
			name:               "simple_remove",
			currentlyStoredIPs: []string{"1.2.3.4"},
			previousResult: createNewState(map[string]int{
				"1.2.3.4": 0,
			}),
			resolvedIPs: []string{},
			toAdd:       []string{},
			toRemove:    []string{"1.2.3.4"},
			newState: state.State{
				Version: 1,
			},
		}, {
			name:               "add_one_remove_one",
			currentlyStoredIPs: []string{"1.2.3.4"},
			previousResult: createNewState(map[string]int{
				"1.2.3.4": 0,
			}),
			resolvedIPs: []string{
				"1.2.3.5",
			},
			toAdd: []string{
				"1.2.3.5",
			},
			toRemove: []string{"1.2.3.4"},
			newState: state.State{
				Version: 1,
				Targets: []state.Target{
					{
						IP:           "1.2.3.5",
						TimesMissing: 0,
					},
				},
			},
		}, {
			name:               "keep_one_another_try",
			currentlyStoredIPs: []string{"1.2.3.4"},
			previousResult: createNewState(map[string]int{
				"1.2.3.4": 0,
			}),
			invocationsBeforeDeregistration: 2,
			resolvedIPs:                     []string{},
			toAdd:                           []string{},
			toRemove:                        []string{},
			newState: state.State{
				Version: 1,
				Targets: []state.Target{
					{
						IP:           "1.2.3.4",
						TimesMissing: 1,
					},
				},
			},
		}, {
			name:               "remove_second_try",
			currentlyStoredIPs: []string{"1.2.3.4"},
			previousResult: state.State{
				Version: 1,
				Targets: []state.Target{
					{
						IP:           "1.2.3.4",
						TimesMissing: 1,
					},
				},
			},
			invocationsBeforeDeregistration: 2,
			resolvedIPs:                     []string{},
			toAdd:                           []string{},
			toRemove:                        []string{"1.2.3.4"},
			newState: state.State{
				Version: 2,
			},
		}, {
			name:               "corrects_itself",
			currentlyStoredIPs: []string{"1.2.3.4"},
			resolvedIPs: []string{
				"1.2.3.4", "1.2.3.5",
			},
			previousResult: state.State{
				Version: 1,
				Targets: []state.Target{
					{
						IP:           "1.2.3.4",
						TimesMissing: 1,
					},
				},
			},
			invocationsBeforeDeregistration: 2,
			toAdd:                           []string{"1.2.3.5"},
			toRemove:                        []string{},
			newState: state.State{
				Version: 2,
				Targets: []state.Target{
					{
						IP:           "1.2.3.4",
						TimesMissing: 0,
					}, {
						IP:           "1.2.3.5",
						TimesMissing: 0,
					},
				},
			},
		},
	}
	for _, run := range runs {
		run := run
		t.Run(run.name, func(t *testing.T) {
			toRemove, toAdd, newState := resolve(run.previousResult, run.currentlyStoredIPs, run.resolvedIPs, run.invocationsBeforeDeregistration, run.removeUnknownIPs)
			require.Equal(t, run.toRemove, toRemove)
			require.Equal(t, run.toAdd, toAdd)
			require.Equal(t, run.newState, newState)
		})
	}
}

func TestMultiResolver(t *testing.T) {
	ctx := context.Background()
	m := NewMultiResolver(nil, nil)
	ip, err := m.LookupIPAddr(ctx, "www.google.com")
	require.NoError(t, err)
	require.True(t, len(ip) > 0)
}
