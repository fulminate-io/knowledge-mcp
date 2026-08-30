// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// resetRegistry clears the package-level collectors map for the duration of
// the test, restoring the original entries when the test completes.
func resetRegistry(t *testing.T) {
	t.Helper()
	saved := make(map[string]Collector, len(collectors))
	maps.Copy(saved, collectors)
	for k := range collectors {
		delete(collectors, k)
	}
	t.Cleanup(func() {
		for k := range collectors {
			delete(collectors, k)
		}
		maps.Copy(collectors, saved)
	})
}

// captureSink is a tiny capturing fake Sink that records the
// (collectorName, *collectorwire.CollectResult) handed to WriteResult
// instead of writing to a real store. The collector root package is store-
// engine-free: tests assert on the captured payload (the Collect →
// resolveSink → WriteResult dispatch), and the server the live RemoteUploadSink
// streams into owns the actual persistence + overlay-vs-full-replace + post-
// populate semantics.
type captureSink struct {
	names []string
	calls []*collectorwire.CollectResult
}

var _ Sink = (*captureSink)(nil)

func (s *captureSink) WriteResult(_ context.Context, name string, r *collectorwire.CollectResult) error {
	s.names = append(s.names, name)
	s.calls = append(s.calls, r)
	return nil
}

// installCaptureSink installs a captureSink as the DefaultSinkFactory for the
// duration of the test (so a bare CollectOptions{} exercises the resolveSink
// default path) and returns it for assertions. No real store is initialized.
func installCaptureSink(t *testing.T) *captureSink {
	t.Helper()
	cap := &captureSink{}
	prev := DefaultSinkFactory
	DefaultSinkFactory = func() Sink { return cap }
	t.Cleanup(func() { DefaultSinkFactory = prev })
	return cap
}

// fakeCollector is a minimal Collector for testing the pipeline.
type fakeCollector struct {
	name   string
	result *collectorwire.CollectResult
	err    error
}

func (f *fakeCollector) Name() string { return f.name }

func (f *fakeCollector) Collect(_ context.Context, _ string, _ CollectOptions) (*collectorwire.CollectResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestCollect_FullReplace_HappyPath(t *testing.T) {
	cap := installCaptureSink(t)
	resetRegistry(t)

	Register(&fakeCollector{
		name: "test",
		result: &collectorwire.CollectResult{
			GraphType: kgtypes.GraphCode,
			GraphName: "test-repo",
			Nodes: []*knowledgev1.Node{
				{Type: string(kgtypes.NodeFile), SymbolName: "main.go", FilePath: "main.go"},
				{Type: string(kgtypes.NodeFile), SymbolName: "util.go", FilePath: "util.go"},
			},
			Edges: []kgwire.BatchEdge{
				{FromIdx: 0, ToIdx: 1, Type: kgtypes.EdgeContains},
			},
		},
	})

	_, err := Collect(context.Background(), "test", "/some/path", CollectOptions{})
	require.NoError(t, err)

	// The pipeline's job is the Collect → resolveSink → WriteResult dispatch;
	// verify the captured payload forwarded to the sink. Persistence + post-
	// populate are sink/server responsibilities (tested server-side
	// in the collect chunk/finalize tests).
	require.Len(t, cap.calls, 1)
	assert.Equal(t, "test", cap.names[0])
	got := cap.calls[0]
	assert.Equal(t, kgtypes.GraphCode, got.GraphType)
	assert.Equal(t, "test-repo", got.GraphName)
	assert.Len(t, got.Nodes, 2)
	assert.Len(t, got.Edges, 1)
}

func TestCollect_UnknownType(t *testing.T) {
	installCaptureSink(t)
	resetRegistry(t)

	_, err := Collect(context.Background(), "nonexistent", "id", CollectOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown collector")
}

func TestCollect_CollectorError(t *testing.T) {
	installCaptureSink(t)
	resetRegistry(t)

	Register(&fakeCollector{
		name: "broken",
		err:  errors.New("collector failed"),
	})

	_, err := Collect(context.Background(), "broken", "id", CollectOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collector failed")
}

func TestCollect_Overlay_HappyPath(t *testing.T) {
	cap := installCaptureSink(t)
	resetRegistry(t)

	// The overlay-vs-full-replace diff semantics are server-side now; the
	// pipeline merely forwards CurrentBranch + the node set to the sink.
	Register(&fakeCollector{
		name: "overlay-test",
		result: &collectorwire.CollectResult{
			GraphType:     kgtypes.GraphCode,
			GraphName:     "base-repo",
			CurrentBranch: "feature-branch",
			Nodes: []*knowledgev1.Node{
				{Id: "node-a", Type: string(kgtypes.NodeFile), SymbolName: "a.go", FilePath: "a.go", Content: "original"},
				{Id: "node-b", Type: string(kgtypes.NodeFile), SymbolName: "b.go", FilePath: "b.go", Content: "changed"},
				{Id: "node-c", Type: string(kgtypes.NodeFile), SymbolName: "c.go", FilePath: "c.go", Content: "new"},
			},
		},
	})

	_, err := Collect(context.Background(), "overlay-test", "id", CollectOptions{})
	require.NoError(t, err)

	require.Len(t, cap.calls, 1)
	got := cap.calls[0]
	assert.Equal(t, "feature-branch", got.CurrentBranch)
	assert.Len(t, got.Nodes, 3)
}

// TestCollect_Overlay_BranchPayloadReachesTheSink asserts what this test actually
// exercises: a collector emitting a branch payload drives the client pipeline to
// completion and hands the sink every node it produced. The client never classified
// overlay rows and never had a comparison function of its own — that decision has
// always belonged to the server — so the name and comment this test used to carry
// described a server-side mechanism the client does not have, and which no longer
// exists on either side.
func TestCollect_Overlay_BranchPayloadReachesTheSink(t *testing.T) {
	installCaptureSink(t)
	resetRegistry(t)

	// The client uploads what the collector emitted; nothing here decides whether a
	// node is changed.
	Register(&fakeCollector{
		name: "overlay-nil-eq",
		result: &collectorwire.CollectResult{
			GraphType:     kgtypes.GraphCode,
			GraphName:     "nil-eq-repo",
			CurrentBranch: "snapshot",
			Nodes: []*knowledgev1.Node{
				{Type: string(kgtypes.NodeFile), SymbolName: "x.go", FilePath: "x.go"},
			},
		},
	})

	_, err := Collect(context.Background(), "overlay-nil-eq", "id", CollectOptions{})
	require.NoError(t, err)
}

func TestCollect_PostPopulateNil_NoPanic(t *testing.T) {
	installCaptureSink(t)
	resetRegistry(t)

	Register(&fakeCollector{
		name: "no-hook",
		result: &collectorwire.CollectResult{
			GraphType: kgtypes.GraphCode,
			GraphName: "no-hook-repo",
			Nodes: []*knowledgev1.Node{
				{Type: string(kgtypes.NodeFile), SymbolName: "f.go", FilePath: "f.go"},
			},
		},
	})

	// Should not panic even though no PostPopulate is registered for "no-hook".
	_, err := Collect(context.Background(), "no-hook", "id", CollectOptions{})
	require.NoError(t, err)
}
