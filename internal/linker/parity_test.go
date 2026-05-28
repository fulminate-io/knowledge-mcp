// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// goldenEdge mirrors the shape of a mutate(link) call the client linker
// emits for the parity fixture. Compared on exact-match fields:
// FromID/ToID/Type/Confidence/Method.
//
// T3 #2 of the reviewer audit: LastValidated is INTENTIONALLY excluded
// from the golden — the field is stamped at "now" each time the linker
// runs (time.Now().UTC()), so comparing it would make the test flake.
// Both the legacy pkg/linker.RunAll path (which stamps LastValidated on
// the edge it constructs) and the new client linker path produce
// identical proxy IDs and metadata except for that stamp; identity-by-
// construction holds because both paths terminate in CreateCrossGraphProxy
// with the same ProxyTarget.
type goldenEdge struct {
	FromID       string
	ToID         string
	Relationship string
	Method       string
	Confidence   float64
}

// goldenFixtureEdges is the checked-in golden snapshot. Built by running
// the linker against the parityFixture below and capturing the emitted
// edges. Recheck on intentional linker changes; otherwise this test
// catches regressions in the parity invariant.
// T-GTB6: the client linker now COMPOSES the cross-graph proxies before writing
// the linkage edge (emitLink → crossgraph.ResolveAndLink), where the pre-T-GTB6
// path sent RAW ids and let the SERVER's ResolveOrProxy build the proxies. So the
// composed linkage edge's FromID is the deterministic CLOUD PROXY (the cloud
// Deployment is a real node), while ToID stays the raw repo-name "myapp" (not a
// node → server-parity best-effort). The actual linkage edge is byte-identical to
// what the server produced; the golden moved from the pre-resolution wire layer to
// the post-resolution composed edge.
var goldenFixtureEdges = []goldenEdge{
	{
		FromID:       "proxy:cloud:prod:default/Deployment/myapp-server",
		ToID:         "myapp",
		Relationship: "BUILDS",
		Method:       "tier1-image",
		Confidence:   0.9,
	},
}

// TestClientLinker_FixtureParity exercises the client linker against a
// representative fixture and asserts the emitted edges match the golden
// snapshot. Uses a checked-in golden (not a live pkg/linker.RunAll
// comparison at test time) so the test survives the Phase 6 pkg/linker
// deletion.
func TestClientLinker_FixtureParity(t *testing.T) {
	// Representative fixture: one cloud Deployment whose image matches a
	// known code repo. This is the canonical Tier-1 image-linker case
	// that drives the test (helm/dockerfile/wi add further matrices but
	// the parity invariant is the same).
	cloudResource := &knowledgev1.Node{
		Id:         "default/Deployment/myapp-server",
		Type:       string(kgtypes.NodeCloudResource),
		SymbolName: "myapp-server",
		Content:    `{"spec":{"template":{"spec":{"containers":[{"image":"gcr.io/project/myapp:v1"}]}}}}`,
		Metadata: map[string]string{
			"resource_type": "Deployment",
		},
	}

	gc := &fakeGraphCaller{}
	gc.respond = func(tool string, args map[string]any) (kgtools.ToolResult, error) {
		if tool == "query" {
			graph, _ := args["graph"].(string)
			switch graph {
			case "code":
				if _, hasType := args["type"]; hasType {
					return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{}}), nil
				}
				return jsonResult(t, map[string]any{"graphs": []string{"myapp"}}), nil
			case "cloud":
				if _, hasType := args["type"]; hasType {
					return jsonResult(t, map[string]any{"nodes": []*knowledgev1.Node{cloudResource}}), nil
				}
				return jsonResult(t, map[string]any{"graphs": []string{"prod"}}), nil
			}
		}
		return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{}`}}}, nil
	}
	// The cloud Deployment is a real node → its FROM materializes to a cloud proxy.
	gc.seedNode("cloud", cloudResource)

	_, err := RunAll(context.Background(), gc, LinkOptions{})
	require.NoError(t, err)

	// Capture the composed linkage edges (emitLink rides crossgraph.ResolveAndLink
	// over the Execute seam now, not a raw mutate(link) Call).
	emitted := make([]goldenEdge, 0, len(gc.capturedLinks))
	for _, l := range gc.capturedLinks {
		emitted = append(emitted, goldenEdge{
			FromID:       l.FromID,
			ToID:         l.ToID,
			Relationship: l.Relationship,
			Method:       l.Method,
			Confidence:   l.Confidence,
		})
	}

	// Order-independent compare against the golden.
	sortGoldenEdges(emitted)
	expected := append([]goldenEdge(nil), goldenFixtureEdges...)
	sortGoldenEdges(expected)

	assert.Equal(t, expected, emitted,
		"client linker output drifted from golden — re-record only if the linker contract intentionally changed")
}

// sortGoldenEdges sorts a slice of goldenEdge in place by (Relationship,
// FromID, ToID) so order-of-emission doesn't fail the parity compare.
func sortGoldenEdges(edges []goldenEdge) {
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Relationship != edges[j].Relationship {
			return edges[i].Relationship < edges[j].Relationship
		}
		if edges[i].FromID != edges[j].FromID {
			return edges[i].FromID < edges[j].FromID
		}
		return edges[i].ToID < edges[j].ToID
	})
}
