// SPDX-License-Identifier: Apache-2.0

// pipeline_stop_stage_test.go — Stop names the stage that did not drain
// (GitHub issue #172, requirement R5).
//
// The shutdown budget gives this stage a bounded window, and the daemon now logs
// what Stop returns instead of discarding it. A bare context error would tell an
// operator that something did not finish and nothing about what: collectors still
// pushing work, dispatchers still batching it, and workers still inside an LLM call
// are three different problems with three different remedies.
//
// EACH CASE WEDGES EXACTLY ONE WAIT GROUP and gives Stop a SHORT window rather
// than an already-closed one. An already-closed window is not deterministic here:
// every earlier stage's wait then has both of its select arms ready at once — its
// own wait group is already at zero AND the context is already done — so Go's
// random select choice decides which stage is blamed, and the first run of this
// test blamed the collectors for a wedge in the workers. A live window makes each
// earlier wait take its own arm and only the wedged stage reach the timeout.

package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStopNamesTheStageThatDidNotDrain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		wedge func(p *Pipeline) *sync.WaitGroup
		want  string
	}{
		{name: "collectors", wedge: func(p *Pipeline) *sync.WaitGroup { return &p.collectorWG }, want: "collectors did not drain"},
		{name: "dispatchers", wedge: func(p *Pipeline) *sync.WaitGroup { return &p.dispatcherWG }, want: "dispatchers did not drain"},
		{name: "workers", wedge: func(p *Pipeline) *sync.WaitGroup { return &p.workerWG }, want: "workers did not drain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New(Config{}, nil, nil, nil)
			wg := tc.wedge(p)
			wg.Add(1) // this stage cannot finish inside the window.
			// RELEASED AFTER THE ASSERTIONS, not never: waitWithCtx leaves a
			// goroutine parked on this wait group when its context wins the race,
			// and this package's goleak check would report that parked goroutine as
			// a leak of the code under test rather than of the wedge.
			t.Cleanup(wg.Done)

			window, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()

			err := p.Stop(window)
			require.Error(t, err, "a stage that did not drain inside the window must be reported")
			require.ErrorIs(t, err, context.DeadlineExceeded, "the report must carry the context error it came from")
			require.Contains(t, err.Error(), tc.want,
				"the error must name the stage; %q tells a reader which subsystem was still running at exit", tc.want)
		})
	}

	t.Run("known_positive_a_pipeline_with_nothing_running_stops_clean", func(t *testing.T) {
		p := New(Config{}, nil, nil, nil)
		require.NoError(t, p.Stop(t.Context()),
			"CONTROL: without this, an error-naming assertion would be satisfied by a Stop that always fails")
	})
}
