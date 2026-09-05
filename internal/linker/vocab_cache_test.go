// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestLinkerVocabularyIsReadOncePerPass is the linker seam's COST gate: one
// vocabulary read per PASS, however many edges the pass emits.
//
// It drives LinkDockerfiles at two very different edge counts, because a gate
// run at a single N cannot tell "once per pass" from "once per edge" — at N=1
// they are the same number. The RATIO is the observable.
func TestLinkerVocabularyIsReadOncePerPass(t *testing.T) {
	for _, n := range []int{5, 25} {
		t.Run(fmt.Sprintf("%d COPY directives cost one vocabulary read", n), func(t *testing.T) {
			gc := dockerfilePassFixture(t, n)

			count, err := LinkDockerfiles(context.Background(), gc, LinkOptions{})
			require.NoError(t, err)

			// ASSERT THE CAPTURED LINKS, NOT THE PASS'S RETURN VALUE. The two
			// are not interchangeable: LinkDockerfiles counts would-emit
			// directives — linkSingleDockerfile increments after emitLink
			// returns nil, and emitLink returns nil EARLY on DryRun without
			// composing anything — so the return value equals N even on a pass
			// that composes nothing at all. Measured on exactly such a pass:
			// returnValue=5, capturedLinks=0, statsCalls=1, where a
			// return-value assertion PASSES and this one fails. Asserting the
			// captured LINK count is what stops this ratio gate being satisfied
			// by an emit-nothing pass.
			require.Len(t, gc.capturedLinks, n,
				"the pass must actually COMPOSE n linkage edges, not merely count n would-emit directives")
			assert.Equal(t, n, count, "the pass reports the edges it emitted")

			assert.Equal(t, 1, gc.statsCalls,
				"the linkage vocabulary is read ONCE per pass, not once per emitted edge")
		})
	}
}

// TestVocabCacheReadsUnderlyingSeamOnce pins the CACHE ITSELF, which is the
// second and independent observable behind the ratio gate above.
//
// The two are not redundant. The gate above asserts what a PASS costs, read off
// the fake; this asserts that the wrapper is WHY — the underlying seam is
// entered exactly once no matter how many Stats calls arrive, and its counter
// is incremented inside the fetch rather than in the accessor, so the number is
// a measurement rather than a restatement of what sync.Once guarantees
// structurally. It also drives the ERROR arm, which the pass-level gate's
// fixture never reaches: a FAILED first fetch is memoized exactly as a
// successful one is, so a pass can never resolve different edges against
// different vocabulary snapshots.
func TestVocabCacheReadsUnderlyingSeamOnce(t *testing.T) {
	t.Run("a successful snapshot is served to every later call", func(t *testing.T) {
		inner := &fakeGraphCaller{edgesByType: map[string]int64{"BUILDS": 3}}
		v, ok := withVocabCache(inner).(*vocabCachingCaller)
		require.True(t, ok, "withVocabCache returns the caching wrapper")

		for range 5 {
			resp, err := v.Stats(context.Background(), &knowledgev1.StatsRequest{})
			require.NoError(t, err)
			assert.Equal(t, map[string]int64{"BUILDS": 3}, resp.GetGraphStats().GetEdgesByType())
		}
		assert.Equal(t, 1, v.reads, "the wrapper entered its fetch exactly once across five calls")
		assert.Equal(t, 1, inner.statsCalls, "and the UNDERLYING seam saw exactly one call")
	})

	t.Run("a FAILING first fetch is memoized too, so the pass sees one snapshot", func(t *testing.T) {
		inner := &failingOnceCaller{}
		v, ok := withVocabCache(inner).(*vocabCachingCaller)
		require.True(t, ok)

		for i := range 5 {
			_, err := v.Stats(context.Background(), &knowledgev1.StatsRequest{})
			require.Error(t, err, "call %d must see the memoized failure, not a retry that succeeds", i)
		}
		assert.Equal(t, 1, v.reads, "the wrapper does not retry a failed fetch")
		assert.Equal(t, 1, inner.calls,
			"caching the error is what stops one pass resolving different edges against different vocabulary snapshots")
	})
}

// callCaller is a wider seam than the wrapper carries: Call is a method the
// CONCRETE client implements and the embedded GraphCaller INTERFACE does not
// promote. Declared here, in the test, because no production path asserts it —
// its only job is to be the control that makes the assertion below
// discriminating rather than self-satisfying.
type callCaller interface {
	Call(ctx context.Context, tool string, rawArgs json.RawMessage) (kgtools.ToolResult, error)
}

// TestVocabCacheWrapperMethodSetIsExecutePlusStats pins the method set the type's
// doc comment claims, so the claim cannot rot back into the transparency it used
// to assert.
//
// The two positive legs are the whole reachable surface: Execute and Stats are
// the ONLY assertions any wrapped value meets in this corpus — Execute-only via
// asExecutor here, crossgraph.GraphCaller and the render fetch helpers, and Stats
// via linkerStatsFnOf. The NEGATIVE leg is what stops the positives being
// vacuous: the same fake carries Call, the unwrapped value satisfies it, and the
// wrapped one does not, which is exactly the Go rule the old comment denied.
func TestVocabCacheWrapperMethodSetIsExecutePlusStats(t *testing.T) {
	inner := &fakeGraphCaller{}
	wrapped := withVocabCache(inner)

	_, servesExecute := wrapped.(linkerExecutor)
	assert.True(t, servesExecute, "a wrapped value still satisfies the Execute seam (asExecutor, crossgraph, render)")
	_, servesStats := wrapped.(statsCaller)
	assert.True(t, servesStats, "a wrapped value satisfies the Stats seam (linkerStatsFnOf)")

	// KNOWN-POSITIVE CONTROL, same run, same instrument: without it, both
	// assertions above would also pass on a wrapper that promoted everything,
	// and the leg would prove nothing about what embedding an interface costs.
	_, unwrappedServesCall := any(inner).(callCaller)
	require.True(t, unwrappedServesCall, "control: the UNWRAPPED client does carry Call")
	_, wrappedServesCall := wrapped.(callCaller)
	assert.False(t, wrappedServesCall,
		"embedding the INTERFACE promotes only its own methods, so Call is dropped — widen this wrapper rather than assuming it is transparent")
}

// failingOnceCaller fails its FIRST Stats call and succeeds afterwards. It is
// the discriminating fixture for the error-memo decision: under a cache that
// memoized only successes this caller would be entered twice and four of five
// edges would compose, which is exactly the mixed-snapshot pass the memo exists
// to prevent.
type failingOnceCaller struct {
	fakeGraphCaller
	calls int
}

func (f *failingOnceCaller) Stats(_ context.Context, _ *knowledgev1.StatsRequest) (*knowledgev1.StatsResponse, error) {
	f.calls++
	if f.calls == 1 {
		return nil, errors.New("stats backend unavailable")
	}
	return &knowledgev1.StatsResponse{GraphStats: &knowledgev1.GraphStats{}}, nil
}

// dockerfilePassFixture builds a fake serving ONE Dockerfile with n COPY
// directives and the n source files they name, so a single pass emits exactly n
// linkage edges.
func dockerfilePassFixture(t *testing.T, n int) *fakeGraphCaller {
	t.Helper()

	var body strings.Builder
	body.WriteString("FROM scratch\n")
	nodes := make([]*knowledgev1.Node, 0, n+1)
	dfNode := &knowledgev1.Node{
		Id:       "myapp:Dockerfile",
		Type:     string(kgtypes.NodeFile),
		FilePath: "Dockerfile",
	}
	nodes = append(nodes, dfNode)
	srcs := make([]*knowledgev1.Node, 0, n)
	for i := range n {
		path := fmt.Sprintf("src%d.go", i)
		fmt.Fprintf(&body, "COPY %s /\n", path)
		src := &knowledgev1.Node{
			Id:       "myapp:" + path,
			Type:     string(kgtypes.NodeFile),
			FilePath: path,
		}
		srcs = append(srcs, src)
		nodes = append(nodes, src)
	}
	dfNode.Content = body.String()

	gc := &fakeGraphCaller{}
	gc.respond = func(tool string, args map[string]any) (kgtools.ToolResult, error) {
		if tool == "query" {
			if graph, _ := args["graph"].(string); graph == "code" {
				if typ, hasType := args["type"].(string); hasType {
					if typ == string(kgtypes.NodeFile) {
						return jsonResult(t, map[string]any{"nodes": nodes}), nil
					}
					return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{}}), nil
				}
				return jsonResult(t, map[string]any{"graphs": []string{"myapp"}}), nil
			}
		}
		return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
	}
	gc.seedNode("code", dfNode)
	for _, s := range srcs {
		gc.seedNode("code", s)
	}
	return gc
}
