// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// compFailStubType is the unique stub collector type whose declared composition
// invariant always refuses, so builtinCollectWork's wiring of the verdict is
// observable end to end.
const compFailStubType = "composition-verdict-test"

var compFailStubOnce sync.Once

// compFailStubCollector succeeds at collecting — the nodes ARE written — and then
// declares its own composition unusable. That is the exact shape of the incident
// this guard exists for: a harvest that ran cleanly and produced nothing usable.
type compFailStubCollector struct{}

func (compFailStubCollector) Name() string { return compFailStubType }

func (compFailStubCollector) Collect(_ context.Context, _ string, _ collector.CollectOptions) (*collectorwire.CollectResult, error) {
	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphCloud,
		GraphName: "verdict-smoke",
		Nodes: []*knowledgev1.Node{
			{Type: "list_item"},
			{Type: "list_item"},
			{Type: "page"},
		},
	}, nil
}

func (compFailStubCollector) AssertComposition(c collector.CollectComposition) error {
	return errors.New("collect " + compFailStubType + " " + c.GraphName +
		": harvest captured nothing usable — " + c.Render())
}

var _ collector.CompositionAsserter = compFailStubCollector{}

// TestBuiltinCollectWork_ReportsCompositionFailureAfterTail is the CATCHER for
// omitting the wire-up. It asserts BOTH halves, and the second half is what stops
// the wiring from being satisfiable by an early return that skips the tail:
//
//  1. the composition verdict surfaces as builtinCollectWork's error, carrying
//     the locked substring; AND
//  2. the pipeline wake still fired — so the linker, the postpopulate hook and
//     the wake all ran BEFORE the verdict was returned. The nodes were written;
//     a harvest whose composition is refused must not also suppress the nudge
//     the rows it DID upload need.
func TestBuiltinCollectWork_ReportsCompositionFailureAfterTail(t *testing.T) {
	compFailStubOnce.Do(func() { collector.Register(compFailStubCollector{}) })

	rt := NewCollectRuntime()
	deps := &detachFullDeps{rt: rt, gc: &fakeGraphCaller{}}

	composition, err := builtinCollectWork(context.Background(), deps,
		collectArgs{Type: compFailStubType, ID: "verdict-id"},
		collector.CollectOptions{Sink: noopSink{}})

	require.Error(t, err, "a refused composition must make the collect report failure")
	assert.Contains(t, err.Error(), "harvest captured nothing usable")
	assert.Contains(t, err.Error(), "nodes 3 (list_item 2, page 1), edges 0",
		"the error embeds WHAT was captured in the same line that says it was not usable")

	// The tail ran. WakePipeline is the LAST tail step in builtinCollectWork —
	// strictly after runPostCollectLinker and runPostCollectPostPopulate in
	// straight-line code — so observing it fire proves the whole tail executed
	// before the verdict was returned.
	assert.GreaterOrEqual(t, deps.wake.Load(), int32(1),
		"the pipeline wake must still fire on a composition failure")

	// The composition is returned alongside the error, so the run registry records
	// what the harvest produced even on the failing path.
	assert.Equal(t, "nodes 3 (list_item 2, page 1), edges 0", composition)

	// KNOWN NEGATIVE, same call path and same deps: a collector declaring NO
	// invariant reports success. Without it, a builtinCollectWork that returned an
	// error unconditionally would satisfy every assertion above.
	registerDetachStub()
	detachStubStarted = make(chan struct{})
	detachStubRelease = make(chan struct{})
	close(detachStubRelease)
	okComposition, okErr := builtinCollectWork(context.Background(), deps,
		collectArgs{Type: detachFullPathType, ID: "no-invariant-id"},
		collector.CollectOptions{Sink: noopSink{}})
	require.NoError(t, okErr, "a collector declaring no invariant must still report success")
	assert.Equal(t, "nodes 0, edges 0", okComposition)
}
