// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// timingEmbedder records the wall-clock time of each embedder dispatch so a
// test can assert the proactive RPM gate paced successive calls. It reuses the
// EmbedderFunc shape (injected via New) — the same seam fakeEmbedder uses.
type timingEmbedder struct {
	mu     sync.Mutex
	stamps []time.Time
}

func (e *timingEmbedder) call(_ context.Context, items []EmbedItem) (map[string][]byte, error) {
	e.mu.Lock()
	e.stamps = append(e.stamps, time.Now())
	e.mu.Unlock()
	out := make(map[string][]byte, len(items))
	for _, it := range items {
		out[it.ID] = make([]byte, 32)
	}
	return out, nil
}

func (e *timingEmbedder) gap() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.stamps) < 2 {
		return 0
	}
	return e.stamps[1].Sub(e.stamps[0])
}

// TestEmbedWorker_RPMGatePacesDispatch proves the proactive RPM gate, when
// configured, delays the SECOND embedder dispatch ~one pacing interval relative
// to the first, and that the disabled default (EmbedRPM:0) imposes no
// measurable delay. Two sequential single-item batches are driven through
// runEmbedWorkerBatch (which calls processEmbedGroup → p.embedRPM.wait →
// p.embedder). Timing bands are generous (±20%-style) to avoid CI flake.
func TestEmbedWorker_RPMGatePacesDispatch(t *testing.T) {
	t.Run("enabled_paces_second_dispatch", func(t *testing.T) {
		ctx := context.Background()
		wc := newFakeWireClient()
		te := &timingEmbedder{}
		// rpm=120 ⇒ 500ms interval. First dispatch is immediate; the second
		// is paced ~one interval after it.
		interval := time.Minute / 120
		p := New(Config{EmbedRPM: 120}, wc, nil, te.call)

		runEmbedWorkerBatch(ctx, p, []EmbedWork{embedWork("f1", "first")})
		runEmbedWorkerBatch(ctx, p, []EmbedWork{embedWork("f2", "second")})

		gap := te.gap()
		assert.GreaterOrEqual(t, gap, time.Duration(0.8*float64(interval)),
			"enabled gate must delay the second dispatch ~one pacing interval")
	})

	t.Run("disabled_no_delay", func(t *testing.T) {
		ctx := context.Background()
		wc := newFakeWireClient()
		te := &timingEmbedder{}
		p := New(Config{EmbedRPM: 0}, wc, nil, te.call) // disabled — current default

		runEmbedWorkerBatch(ctx, p, []EmbedWork{embedWork("f1", "first")})
		runEmbedWorkerBatch(ctx, p, []EmbedWork{embedWork("f2", "second")})

		gap := te.gap()
		assert.Less(t, gap, 50*time.Millisecond,
			"disabled gate must impose no measurable pacing delay")
	})
}
