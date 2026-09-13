// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
)

// collect_deletion_refusal_text_test.go — the LAST HOP of the refusal report: a
// deletion the server refused reaches the caller's result text.
//
// WHY IT HAS TO BE TESTED HERE. The sink learns the refusal and records it; only
// this layer renders text. A sink test can prove the recorder was written and
// still leave the calling LLM reading a plain success line, because the composition
// this handler returns is computed BEFORE the sink ever runs.

// refusingSink is a Sink that records a server refusal, exactly as the real
// UploadSink does when Finalize answers with a reason. The refusal the server
// returns is not a sink error — the rows the collect uploaded landed — so this
// double returns nil like the real thing.
type refusingSink struct{ reason string }

func (s refusingSink) WriteResult(ctx context.Context, _ string, _ *collectorwire.CollectResult) error {
	collector.RecordDeletionRefusal(ctx, s.reason, 201)
	return nil
}

// TestBuiltinCollectWork_SurfacesDeletionRefusalInTheResultText pins both
// directions in one run: a refusal is appended to the composition text, and an
// admitted collect's text is byte-identical to what it was before the report
// existed.
func TestBuiltinCollectWork_SurfacesDeletionRefusalInTheResultText(t *testing.T) {
	registerDetachStub()
	detachStubStarted = make(chan struct{})
	detachStubRelease = make(chan struct{})
	close(detachStubRelease)

	rt := NewCollectRuntime()
	deps := &detachFullDeps{rt: rt, gc: &fakeGraphCaller{}}

	refused, _, err := collectWork(context.Background(), deps,
		collectArgs{Type: detachFullPathType, ID: "refused-id"},
		collector.CollectOptions{Sink: refusingSink{reason: "walk_incomplete"}}, "", false,
		builtinCollectRunner())
	require.NoError(t, err, "a refused DELETION is not a failed collect")
	assert.Contains(t, refused, "nodes 0, edges 0",
		"the composition the collect produced must still be reported")
	assert.Contains(t, refused, "DELETION REFUSED",
		"a refused deletion must reach the caller's result text — a plain success line is what hid it")
	assert.Contains(t, refused, "walk_incomplete", "and the text must name the guard that refused")

	// THE CONTROL, same call path and same deps: a sink that records nothing leaves
	// the text exactly as it was. Without it, an implementation that appended the
	// notice unconditionally would pass everything above.
	detachStubStarted = make(chan struct{})
	admitted, _, okErr := collectWork(context.Background(), deps,
		collectArgs{Type: detachFullPathType, ID: "admitted-id"},
		collector.CollectOptions{Sink: noopSink{}}, "", false, builtinCollectRunner())
	require.NoError(t, okErr)
	assert.Equal(t, "nodes 0, edges 0", admitted,
		"an admitted collect's result text must carry no refusal notice at all")
}
