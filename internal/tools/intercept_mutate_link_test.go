// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/crossgraph"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// graphNodeResult seeds a single node body keyed by id for the graph-aware fake
// Execute ByID probe. typ + the optional symbol/summary fields let
// BuildCrossGraphProxy derive the proxy from the practice TO node.
func graphNodeResult(t *testing.T, id, typ, symbol, summary string) kgtools.ToolResult {
	t.Helper()
	payload := map[string]any{"id": id, "type": typ, "symbol_name": symbol, "summary": summary}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(b)}}}
}

// TestCrossGraphLink_KnowledgeFromPracticeTo_MaterializesProxy covers criterion
// 231b7957 (A): a mutate(link, graph:practice, from:<knowledge-id>, to:<practice-
// id>, relationship:uses, language:go) where FROM resolves in KNOWLEDGE and TO in
// practice/go materializes the deterministic proxy via the engine Execute seam (a
// MUTATION_KIND_UPSERT with NodeBody.Id=='proxy:practice:go:<to>') + a from→proxy
// MUTATION_KIND_LINK Execute targeting knowledge. A SECOND identical call is
// idempotent (same proxy id).
func TestCrossGraphLink_KnowledgeFromPracticeTo_MaterializesProxy(t *testing.T) {
	fc := &fakeGraphCaller{
		// FROM (dec-1) in knowledge; TO (pat-1) only in practice/go (name-aware).
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "a decision")},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "practice", Name: "go"}: {"pat-1": graphNodeResult(t, "pat-1", "pattern", "Pat", "a pattern")},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"practice", "go"}),
	}
	deps := interceptTestDeps{gc: fc}

	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","graph":"practice","language":"go","from":"dec-1","to":"pat-1","relationship":"Uses"}`),
	})
	require.True(t, handled, "knowledge-FROM/practice-TO proxy link must be claimed client-side")
	require.False(t, res.IsError, "proxy link: %s", toolResultText(res))

	// Two Mutation Executes: the proxy UPSERT then the from→proxy LINK.
	require.Len(t, fc.execMutations, 2, "one UPSERT + one LINK")

	upsert := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, upsert.GetKind())
	require.Len(t, upsert.GetNodeBodies(), 1)
	assert.Equal(t, "proxy:practice:go:pat-1", upsert.GetNodeBodies()[0].GetId(), "deterministic practice proxy id")
	assert.Equal(t, "proxy", upsert.GetNodeBodies()[0].GetType())
	assert.Equal(t, "proxy:practice:go", upsert.GetNodeBodies()[0].GetSource())

	link := fc.execMutations[1]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_LINK, link.GetKind())
	assert.Equal(t, []string{"dec-1"}, link.GetSelection().GetIds(), "from is the edge source")
	assert.Equal(t, "uses", link.GetEdgeSpec().GetRelationship(), "knowledge edges are lowercase")
	assert.Equal(t, "proxy:practice:go:pat-1", link.GetEdgeSpec().GetToId())

	// The LINK Execute targets the knowledge graph (proxy + edge live there).
	require.NotEmpty(t, fc.execRequests)
	lastTarget := fc.execRequests[len(fc.execRequests)-1].GetTarget()
	assert.Equal(t, "knowledge", lastTarget.GetGraph())

	// Idempotent: a second identical call reuses the same deterministic proxy id.
	handled2, res2 := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","graph":"practice","language":"go","from":"dec-1","to":"pat-1","relationship":"Uses"}`),
	})
	require.True(t, handled2)
	require.False(t, res2.IsError)
	require.Len(t, fc.execMutations, 4, "second call → another UPSERT+LINK, same ids")
	assert.Equal(t, "proxy:practice:go:pat-1", fc.execMutations[2].GetNodeBodies()[0].GetId())
}

// TestCrossGraphLink_ProxySlugParity covers criterion 231b7957's slug-parity
// clause: a language with a non-trivial slug ("C++" → "cplusplus") produces the
// byte-identical proxy id the server's slugifyLanguage would (store.Slugify-
// Language is the single shared rule).
func TestCrossGraphLink_ProxySlugParity(t *testing.T) {
	// The practice graph for "C++" is named by its slug ("cplusplus") — that is
	// what listForeignGraphs reports and what locateForeignNode probes. The slug
	// is the deterministic SlugifyLanguage("C++") output, inlined here: the client
	// proxy path uses the graph NAME the fake reports (already a slug), so this
	// test seeds + asserts that slug literally.
	const slug = "cplusplus"
	fc := &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d")},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "practice", Name: slug}: {"pat-1": graphNodeResult(t, "pat-1", "pattern", "Pat", "p")},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"practice", slug}),
	}
	deps := interceptTestDeps{gc: fc}

	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","graph":"practice","language":"C++","from":"dec-1","to":"pat-1","relationship":"uses"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "proxy link: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 2)
	wantID := "proxy:practice:" + slug + ":pat-1"
	assert.Equal(t, "proxy:practice:cplusplus:pat-1", wantID, "slug parity: C++ → cplusplus")
	assert.Equal(t, wantID, fc.execMutations[0].GetNodeBodies()[0].GetId())
}

// TestCrossGraphLink_ProxyEquivalence covers criterion 3f195bfb: the client proxy
// branch produces the SAME proxy node (id, source, foreign_graph/foreign_id/
// language metadata) as the server's resolvePracticeToProxy. Both paths build the
// proxy via the SHARED crossgraph.BuildCrossGraphProxy with a ProxyTarget whose
// Name is the language slug, so equivalence is structural — this asserts the exact
// field shape both paths emit, for a slug-transforming language input (so the
// byte-identical-id parity is exercised, not just "go"). The slug
// ("javascript-typescript") is the deterministic SlugifyLanguage output, inlined.
func TestCrossGraphLink_ProxyEquivalence(t *testing.T) {
	const langSlug = "javascript-typescript"
	const toID = "pat-1"
	// crossgraph.BuildCrossGraphProxy takes the *knowledgev1.Node wire node + the
	// proto *knowledgev1.ProxyTarget directly.
	practiceSrc := &knowledgev1.Node{
		Id:         toID,
		Type:       string(kgtypes.NodePattern),
		SymbolName: "Pat",
		Summary:    "a pattern",
	}
	// The exact ProxyTarget both the server (resolvePracticeToProxy) and the
	// client (materializePracticeProxy) construct.
	target := &knowledgev1.ProxyTarget{
		GraphType: string(kgtypes.GraphPractice),
		Name:      langSlug,
		NodeId:    toID,
	}
	proxy, err := crossgraph.BuildCrossGraphProxy(target, practiceSrc)
	require.NoError(t, err)

	assert.Equal(t, "proxy:practice:javascript-typescript:pat-1", proxy.Id, "deterministic id with the shared slug")
	assert.Equal(t, "proxy:practice:javascript-typescript", proxy.Source)
	assert.Equal(t, string(kgtypes.NodeProxy), proxy.Type)
	assert.Equal(t, "practice", proxy.Metadata["foreign_graph"])
	assert.Equal(t, toID, proxy.Metadata["foreign_id"])
	assert.Equal(t, "javascript-typescript", proxy.Metadata["language"])
}

// ---------------------------------------------------------------------------
// T-GTB5 generalized FROM/TO matrix. Each case asserts the proxy id byte-matches
// store.BuildCrossGraphProxy for the located (type,name) — the server-parity
// invariant for code/cloud/cicd, and the slug-ful decided-correct convention for
// practice. (TestCrossGraphLink_CodeFromFallsThrough was DELETED: a code-FROM is
// now CLAIMED — case 1 below is its positive replacement.)
// ---------------------------------------------------------------------------

// TestCrossGraphLink_CodeFromKnowledgeTo covers case (1): a code-FROM →
// knowledge-TO link materializes the code proxy (proxy:<repo>:<from>) and links
// proxy→to in knowledge. This is the positive replacement for the deleted
// falls-through test — the code-FROM is now claimed, not dangling.
func TestCrossGraphLink_CodeFromKnowledgeTo(t *testing.T) {
	fc := &fakeGraphCaller{
		// FROM is a code-graph id (absent from knowledge); TO is a knowledge id.
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d")},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "code", Name: "knowledge"}: {"path/to/file.go:Symbol": graphNodeResult(t, "path/to/file.go:Symbol", "function", "Symbol", "a func")},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"code", "knowledge"}),
	}
	deps := interceptTestDeps{gc: fc}

	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","from":"path/to/file.go:Symbol","to":"dec-1","relationship":"uses"}`),
	})
	require.True(t, handled, "code-FROM → knowledge-TO is now claimed")
	require.False(t, res.IsError, "code-from link: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 2, "code-proxy UPSERT + proxy→to LINK")
	upsert := fc.execMutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UPSERT, upsert.GetKind())
	wantProxy := mustProxyID(t, kgtypes.GraphCode, "knowledge", "path/to/file.go:Symbol")
	assert.Equal(t, wantProxy, upsert.GetNodeBodies()[0].GetId(), "code proxy id byte-matches BuildCrossGraphProxy")
	assert.Equal(t, "proxy:knowledge:path/to/file.go:Symbol", wantProxy)

	link := fc.execMutations[1]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_LINK, link.GetKind())
	assert.Equal(t, []string{wantProxy}, link.GetSelection().GetIds(), "the code proxy is the edge source")
	assert.Equal(t, "dec-1", link.GetEdgeSpec().GetToId(), "knowledge TO used directly (no proxy)")
}

// TestCrossGraphLink_CloudFromKnowledgeTo covers case (2): a cloud-FROM →
// knowledge-TO link materializes proxy:cloud:<account>:<from>.
func TestCrossGraphLink_CloudFromKnowledgeTo(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d")},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "cloud", Name: "acct-1"}: {"ec2:i-abc": graphNodeResult(t, "ec2:i-abc", "resource", "i-abc", "an instance")},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"cloud", "acct-1"}),
	}
	deps := interceptTestDeps{gc: fc}

	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","from":"ec2:i-abc","to":"dec-1","relationship":"relates-to"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "cloud-from link: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 2)
	wantProxy := mustProxyID(t, kgtypes.GraphCloud, "acct-1", "ec2:i-abc")
	assert.Equal(t, "proxy:cloud:acct-1:ec2:i-abc", wantProxy)
	assert.Equal(t, wantProxy, fc.execMutations[0].GetNodeBodies()[0].GetId())
	assert.Equal(t, []string{wantProxy}, fc.execMutations[1].GetSelection().GetIds())
	assert.Equal(t, "dec-1", fc.execMutations[1].GetEdgeSpec().GetToId())
}

// TestCrossGraphLink_KnowledgeFromCloudTo covers case (3): a knowledge-FROM →
// cloud-TO link materializes proxy:cloud:<account>:<to> + from→proxy LINK.
func TestCrossGraphLink_KnowledgeFromCloudTo(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d")},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "cloud", Name: "acct-1"}: {"ec2:i-xyz": graphNodeResult(t, "ec2:i-xyz", "resource", "i-xyz", "an instance")},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"cloud", "acct-1"}),
	}
	deps := interceptTestDeps{gc: fc}

	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","from":"dec-1","to":"ec2:i-xyz","relationship":"relates-to"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "knowledge-from cloud-to link: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 2)
	wantProxy := mustProxyID(t, kgtypes.GraphCloud, "acct-1", "ec2:i-xyz")
	assert.Equal(t, "proxy:cloud:acct-1:ec2:i-xyz", wantProxy)
	assert.Equal(t, wantProxy, fc.execMutations[0].GetNodeBodies()[0].GetId())
	assert.Equal(t, []string{"dec-1"}, fc.execMutations[1].GetSelection().GetIds(), "knowledge FROM is the edge source")
	assert.Equal(t, wantProxy, fc.execMutations[1].GetEdgeSpec().GetToId())
}

// TestCrossGraphLink_KnowledgeFromCICDTo covers case (4): a knowledge-FROM →
// cicd-TO link materializes proxy:cicd:<account>:<to>.
func TestCrossGraphLink_KnowledgeFromCICDTo(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d")},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "cicd", Name: "org-1"}: {"workflow:build": graphNodeResult(t, "workflow:build", "workflow", "build", "a workflow")},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"cicd", "org-1"}),
	}
	deps := interceptTestDeps{gc: fc}

	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","from":"dec-1","to":"workflow:build","relationship":"relates-to"}`),
	})
	require.True(t, handled)
	require.False(t, res.IsError, "knowledge-from cicd-to link: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 2)
	wantProxy := mustProxyID(t, kgtypes.GraphCICD, "org-1", "workflow:build")
	assert.Equal(t, "proxy:cicd:org-1:workflow:build", wantProxy)
	assert.Equal(t, wantProxy, fc.execMutations[0].GetNodeBodies()[0].GetId())
	assert.Equal(t, wantProxy, fc.execMutations[1].GetEdgeSpec().GetToId())
}

// TestCrossGraphLink_KnowledgeToKnowledge_Skips covers case (5): a bare
// knowledge↔knowledge link (both endpoints in knowledge, no link_graph) returns
// handled==false BEFORE any pipeline_list_graphs Call (the FROM-first both-in-
// knowledge skip) and issues zero client Execute mutations.
func TestCrossGraphLink_KnowledgeToKnowledge_Skips(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {
				"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d"),
				"dec-2": graphNodeResult(t, "dec-2", "decision", "Dec2", "d2"),
			},
		},
		// No listGraphsResult — assert it is NEVER consulted.
	}
	deps := interceptTestDeps{gc: fc}

	handled, _ := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","from":"dec-1","to":"dec-2","relationship":"relates-to"}`),
	})
	assert.False(t, handled, "both-in-knowledge bare link falls through to server bare-link")
	assert.Empty(t, fc.execMutations, "zero client Execute mutations")
	// Zero pipeline_list_graphs Call — the FROM-first skip runs before listForeignGraphs.
	for _, c := range fc.calls {
		assert.NotEqual(t, "pipeline_list_graphs", c.tool, "no graph enumeration on the knowledge↔knowledge hot path")
	}
}

// TestCrossGraphLink_PracticeFromKnowledgeTo covers case (6): a practice-FROM →
// knowledge-TO link (the direction T-GTB1c left to legacy) is now CLAIMED, with a
// slug-ful FROM proxy proxy:practice:<slug>:<from>.
func TestCrossGraphLink_PracticeFromKnowledgeTo(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d")},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "practice", Name: "go"}: {"pat-1": graphNodeResult(t, "pat-1", "pattern", "Pat", "p")},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"practice", "go"}),
	}
	deps := interceptTestDeps{gc: fc}

	handled, res := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","from":"pat-1","to":"dec-1","relationship":"relates-to"}`),
	})
	require.True(t, handled, "practice-FROM → knowledge-TO is now claimed")
	require.False(t, res.IsError, "practice-from link: %s", toolResultText(res))

	require.Len(t, fc.execMutations, 2)
	wantProxy := mustProxyID(t, kgtypes.GraphPractice, "go", "pat-1")
	assert.Equal(t, "proxy:practice:go:pat-1", wantProxy, "slug-ful FROM proxy")
	assert.Equal(t, wantProxy, fc.execMutations[0].GetNodeBodies()[0].GetId())
	assert.Equal(t, []string{wantProxy}, fc.execMutations[1].GetSelection().GetIds())
	assert.Equal(t, "dec-1", fc.execMutations[1].GetEdgeSpec().GetToId())
}

// TestCrossGraphLink_ForeignFromIdempotent covers case (7): a repeated foreign-
// FROM call reuses the same deterministic proxy id.
func TestCrossGraphLink_ForeignFromIdempotent(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d")},
		},
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "code", Name: "knowledge"}: {"file.go:Fn": graphNodeResult(t, "file.go:Fn", "function", "Fn", "f")},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"code", "knowledge"}),
	}
	deps := interceptTestDeps{gc: fc}
	args := json.RawMessage(`{"operation":"link","from":"file.go:Fn","to":"dec-1","relationship":"uses"}`)

	h1, _ := InterceptMutate(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	h2, _ := InterceptMutate(deps, kgtools.CallToolParams{Name: "mutate", Arguments: args})
	require.True(t, h1)
	require.True(t, h2)
	require.Len(t, fc.execMutations, 4, "two calls → two UPSERT+LINK pairs")
	want := mustProxyID(t, kgtypes.GraphCode, "knowledge", "file.go:Fn")
	assert.Equal(t, want, fc.execMutations[0].GetNodeBodies()[0].GetId())
	assert.Equal(t, want, fc.execMutations[2].GetNodeBodies()[0].GetId(), "same deterministic proxy id on the repeat")
}

// TestCrossGraphLink_UnresolvableFromFallsThrough covers case (8): a FROM absent
// from knowledge AND every foreign graph returns handled==false with zero proxy
// Execute (fall through to legacy — the dangling-edge guard).
func TestCrossGraphLink_UnresolvableFromFallsThrough(t *testing.T) {
	fc := &fakeGraphCaller{
		queryResponsesByGraph: map[string]map[string]kgtools.ToolResult{
			"knowledge": {"dec-1": graphNodeResult(t, "dec-1", "decision", "Dec", "d")},
		},
		// FROM "ghost" resolves in NO graph; the foreign list has one code graph.
		queryResponsesByGraphName: map[graphKey]map[string]kgtools.ToolResult{
			{Type: "code", Name: "knowledge"}: {},
		},
		listGraphsResult: listGraphsResultFor(t, [2]string{"code", "knowledge"}),
	}
	deps := interceptTestDeps{gc: fc}

	handled, _ := InterceptMutate(deps, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"link","from":"ghost","to":"dec-1","relationship":"uses"}`),
	})
	assert.False(t, handled, "an unresolvable FROM falls through to legacy")
	assert.Empty(t, fc.execMutations, "no proxy Execute on the unresolvable-FROM fall-through")
}

// mustProxyID builds the deterministic proxy id the server's BuildCrossGraphProxy
// would emit for the located (type, name, nodeID) — the byte-match invariant.
func mustProxyID(t *testing.T, gt kgtypes.GraphType, name, nodeID string) string {
	t.Helper()
	src := &knowledgev1.Node{Id: nodeID, Type: string(kgtypes.NodeFinding)}
	proxy, err := crossgraph.BuildCrossGraphProxy(
		&knowledgev1.ProxyTarget{GraphType: string(gt), Name: name, NodeId: nodeID}, src)
	require.NoError(t, err)
	return proxy.Id
}
