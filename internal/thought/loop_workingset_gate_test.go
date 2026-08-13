// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// gateRecordingCaller records EVERY request either seam receives, including ones
// it was not programmed for: the Execute arm appends a description of whatever
// arrived rather than quietly returning a zero value, so an unexpected read shows
// up in the stream instead of vanishing into it. That is what makes the empty
// assertions below mean "nothing was issued" rather than "nothing was wired".
type gateRecordingCaller struct {
	mu       sync.Mutex
	requests []string
}

func (c *gateRecordingCaller) record(kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, kind)
}

func (c *gateRecordingCaller) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.requests...)
}

func (c *gateRecordingCaller) has(prefix string) bool {
	for _, r := range c.recorded() {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}

func (c *gateRecordingCaller) Execute(
	_ context.Context, req *knowledgev1.ExecuteRequest,
) (*knowledgev1.ExecuteResponse, error) {
	// CATCH-ALL: any Execute at all is recorded, described by its plan shape, so a
	// read this fake was never taught about is still visible in the stream.
	switch {
	case req.GetMutation() != nil:
		c.record("execute:mutation")
	case req.GetQuery() != nil:
		c.record("execute:query")
	default:
		c.record("execute:unrecognized")
	}
	return &knowledgev1.ExecuteResponse{}, nil
}

func (c *gateRecordingCaller) CorpusDelta(
	_ context.Context, _ *knowledgev1.CorpusDeltaRequest,
) (*knowledgev1.CorpusDeltaResponse, error) {
	c.record("corpus_delta")
	return &knowledgev1.CorpusDeltaResponse{}, nil
}

// gatedLoop builds a loop over the recorder with the working-set gate driven by
// admitted. The corpus scanner is the same recorder, so the CorpusDelta drain the
// tick and the boot detection perform lands in the one request stream.
func gatedLoop(admitted func() bool) (*PropagationLoop, *gateRecordingCaller) {
	rec := &gateRecordingCaller{}
	p := (&PropagationLoop{
		gc:               rec,
		interval:         time.Hour,
		backstopInterval: testBackstopInterval,
		clock:            time.Now,
		stopCh:           make(chan struct{}),
	}).WithCorpusScanner(rec).WithWorkingSetGate(admitted, nil)
	return p, rec
}

// TestBackgroundSurfaces_SkipWhenKnowledgeNotAdmitted drives BOTH background
// entries — the tick and Start(), which is the one an earlier design missed —
// against an EMPTY working set and asserts the recorded request stream is empty
// by equality. The single assertion covers all three reads at once: the boot
// watermark query, the boot cluster detection's CorpusDelta, and the tick drains.
// Deliberately NOT parallel: every entry here claims the process-global
// reflection single-flight guard, so a concurrent pass in another test would
// make this one coalesce and record nothing for the wrong reason.
func TestBackgroundSurfaces_SkipWhenKnowledgeNotAdmitted(t *testing.T) {
	p, rec := gatedLoop(func() bool { return false })

	p.runBackgroundPropagation()

	// Start() spawns the boot path; drive it and give the goroutine a turn.
	p.Start()
	t.Cleanup(func() { p.Stop(time.Second) })
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, []string(nil), rec.recorded(),
		"an unadmitted knowledge graph must receive ZERO requests: no boot watermark read, "+
			"no boot cluster detection, no tick drain")
}

// TestBackgroundSurfaces_RunAfterKnowledgeAdmitted is the known-positive control
// for the zeros above. Without it those zeros would prove only that the fixture
// never wired a backend.
// Deliberately NOT parallel — see the sibling test: the reflection single-flight
// guard is process-global, and a coalesced pass records nothing.
func TestBackgroundSurfaces_RunAfterKnowledgeAdmitted(t *testing.T) {
	var admitted bool
	var mu sync.Mutex
	p, rec := gatedLoop(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return admitted
	})

	p.runBackgroundPropagation()
	require.Equal(t, []string(nil), rec.recorded(),
		"precondition: nothing runs before admission")

	mu.Lock()
	admitted = true
	mu.Unlock()

	p.runBackgroundPropagation()
	assert.NotEmpty(t, rec.recorded(), "an admitted tick must issue its reads")
	assert.True(t, rec.has("corpus_delta"),
		"an admitted tick must drain the corpus, which is the read the gate was suppressing")

	// The boot detection is the second gated entry and takes the same predicate.
	before := len(rec.recorded())
	p.runBootClusterDetection()
	assert.Greater(t, len(rec.recorded()), before,
		"the boot cluster detection must also run once the graph is admitted")
}

// TestForceFullPassStaysUngated pins the USER LEVER outside the rule. The
// assertion is that the stream CONTAINS a CorpusDelta request SPECIFICALLY, not
// merely that it is non-empty, and that precision is what catches both
// mis-placements: a gate pushed down to runPass produces no requests at all
// (which a non-empty check would catch), but a gate pushed to refreshCorpusCache
// leaves runPass issuing its other reads — the browse, the bulk edge read, the
// writeback — so ONLY the CorpusDelta vanishes, and a non-empty check would pass
// while force_full was silently broken.
// Deliberately NOT parallel — see the sibling tests: the reflection single-flight
// guard is process-global.
func TestForceFullPassStaysUngated(t *testing.T) {
	p, rec := gatedLoop(func() bool { return false })

	_, err := p.ForceFullPass(context.Background())
	require.NoError(t, err)

	assert.True(t, rec.has("corpus_delta"),
		"ForceFullPass is a user lever, not a background process: it must still drain the "+
			"corpus with an EMPTY working set. A CorpusDelta specifically, because a gate "+
			"pushed into refreshCorpusCache would remove only this read while the pass's "+
			"other reads kept a non-empty check green")
}
