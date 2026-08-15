// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// TestManageRepairEdges_PreviewEnumeratesAndMutatesNothing asserts the default
// (execute unset) run reports the cross-file CONTAINS fossil and issues ZERO
// mutations, with three controls that must NOT be swept:
//
//   - a SAME-FILE CONTAINS edge (the healthy majority);
//   - a CONTAINS edge whose target carries an EMPTY FilePath — the pathless hub
//     nodes a predicate that only compares strings would sweep;
//   - an INCOMING package→file CONTAINS edge, which the bulk pivot read returns
//     alongside the outgoing ones and whose source is not a file node.
//
// The named fossil is the known positive: without it, "the controls are absent"
// would pass just as well against an enumeration that found nothing at all.
func TestManageRepairEdges_PreviewEnumeratesAndMutatesNothing(t *testing.T) {
	f := newRepairEdgesFake()
	base := f.layer(repairEdgesLayerKey{Repo: "myrepo"})
	base.files = []*knowledgev1.Node{repairFileNode("src/a.ts"), repairFileNode("src/b.ts")}
	base.symbols = map[string]*knowledgev1.Node{
		"pkg/x.go:Foo":    repairSymbolNode("pkg/x.go:Foo", "pkg/x.go"),
		"src/a.ts:Bar":    repairSymbolNode("src/a.ts:Bar", "src/a.ts"),
		"lang:typescript": repairSymbolNode("lang:typescript", ""),
	}
	base.edges = []*knowledgev1.Edge{
		repairContainsEdge("src/a.ts", "pkg/x.go:Foo"),    // the fossil
		repairContainsEdge("src/a.ts", "src/a.ts:Bar"),    // same-file control
		repairContainsEdge("src/b.ts", "lang:typescript"), // empty-FilePath control
		repairContainsEdge("pkg/src", "src/a.ts"),         // incoming package→file control
	}

	handled, res := repairEdgesCall(t, f, `{"operation":"repair_edges","graph":"code","name":"myrepo"}`)
	require.True(t, handled, "repair_edges must be handled by InterceptManage")
	require.False(t, res.IsError, "repair_edges preview: %s", toolResultText(res))
	body := toolResultText(res)

	assert.Contains(t, body, "src/a.ts -> pkg/x.go:Foo (lives in pkg/x.go)",
		"the cross-file CONTAINS fossil is named in the report")
	assert.NotContains(t, body, "src/a.ts:Bar",
		"a same-file CONTAINS edge is healthy containment, not a fossil")
	assert.NotContains(t, body, "lang:typescript",
		"a target with an EMPTY FilePath is excluded — pathless hub nodes must not be swept")
	assert.NotContains(t, body, "pkg/src",
		"an INCOMING package→file CONTAINS edge has a non-file source; the predicate is source-typed")
	assert.Contains(t, body, "code/myrepo: 2 file node(s) scanned, 3 CONTAINS edge(s) examined, 1 fossil(s) found",
		"the denominators and the fossil count are what make a three-digit count readable")
	assert.Contains(t, body, "PREVIEW against the LOCAL backend: nothing was mutated")
	assert.Contains(t, body, `manage(operation:"repair_edges", graph:"code", name:"myrepo", execute:true)`,
		"the preview ends with the exact invocation that would perform the removal")

	assert.Empty(t, f.mutations, "a preview issues ZERO mutations")
}

// TestManageRepairEdges_PreviewReadsInBulk asserts the enumeration costs
// O(pages), not O(files): with 2*BrowsePageSize+7 file nodes each carrying one
// CONTAINS edge to a distinct symbol, the read count is the PAGE arithmetic of
// the three bulk reads. Against an implementation that loops per file this comes
// out at ~1007 rather than 3+3+3.
func TestManageRepairEdges_PreviewReadsInBulk(t *testing.T) {
	const files = 2*paging.BrowsePageSize + 7
	f := newRepairEdgesFake()
	base := f.layer(repairEdgesLayerKey{Repo: "myrepo"})
	for i := range files {
		path := fmt.Sprintf("src/f%05d.ts", i)
		// Every target lives in a DIFFERENT file than its claiming file node, so
		// every edge is a fossil and the hydrate set is the full distinct target
		// set — the widest page arithmetic the reads can produce.
		sym := fmt.Sprintf("pkg/x%05d.go:Sym", i)
		base.files = append(base.files, repairFileNode(path))
		base.symbols[sym] = repairSymbolNode(sym, fmt.Sprintf("pkg/x%05d.go", i))
		base.edges = append(base.edges, repairContainsEdge(path, sym))
	}

	handled, res := repairEdgesCall(t, f, `{"operation":"repair_edges","graph":"code","name":"myrepo"}`)
	require.True(t, handled)
	require.False(t, res.IsError, "repair_edges preview: %s", toolResultText(res))
	assert.Contains(t, toolResultText(res),
		fmt.Sprintf("code/myrepo: %d file node(s) scanned, %d CONTAINS edge(s) examined, %d fossil(s) found", files, files, files),
		"every file across every page reaches the predicate")
	assert.Contains(t, toolResultText(res), fmt.Sprintf("sample capped at %d of %d fossil(s)", repairEdgesSampleCap, files),
		"the render is bounded by the sample cap while the count stays full")

	// The catalog bucket is placed ABOVE the default arm deliberately: overlays-
	// by-default adds ONE RETURN_MODE_GRAPH_NAMES plan per base, which would
	// otherwise land in the hydrate (default) arm and redden the hydrate
	// expectation AGAINST CORRECT WORK.
	var browsePlans, edgePlans, hydratePlans, catalogPlans int
	for _, p := range f.queryPlans {
		switch {
		case p.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES:
			catalogPlans++
		case p.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_EDGES:
			edgePlans++
		case p.GetSelection().GetNodeType() == string(kgtypes.NodeFile):
			browsePlans++
			require.NotNil(t, p.AfterId, "after_id must be PRESENT on every page — presence selects the keyset browse")
			assert.True(t, p.GetSkipTotal(), "a drain page never reads Total")
		default:
			hydratePlans++
		}
	}
	assert.Equal(t, 1, catalogPlans,
		"ONE overlay enumeration for the one named base — no N+1 catalog fan-out")
	assert.Equal(t, files/paging.BrowsePageSize+1, browsePlans,
		"the file browse pages rather than reading the type in one Execute")
	assert.Equal(t, (files+paging.EdgePivotPageSize-1)/paging.EdgePivotPageSize, edgePlans,
		"the edges read is ONE bulk pivot-paged read over the file set, not one traverse per file")
	assert.Equal(t, (files+paging.BrowsePageSize-1)/paging.BrowsePageSize, hydratePlans,
		"the target hydrate is bulk-paged, not one lookup per edge")
	assert.Less(t, len(f.queryPlans), files/10,
		"the whole enumeration costs O(pages); a per-file loop would cost O(files)")
}

// repairEdgesCloudDeps satisfies ClientDeps AND the optional cloudStatusInfo
// view, so the handler's backend-naming and cloud-refusal paths can be driven
// without a live cloud. Same structural-typing discipline manage(status) uses.
type repairEdgesCloudDeps struct {
	interceptTestDeps
	host string
}

func (d repairEdgesCloudDeps) CloudStatusInfo() (bool, string) { return true, d.host }

// TestManageRepairEdges_ExecuteUnlinksOnlyCrossFileContains asserts an execute
// run removes the cross-file CONTAINS fossil and NOTHING else. The three edges
// all leave the SAME file node, so only the predicate distinguishes them:
//
//   - the same-file CONTAINS edge is healthy containment;
//   - the cross-file CALLS edge is a NORMAL call into another file, and is the
//     control that catches a predicate widened from CONTAINS to any edge type.
//
// The fake really applies the unlink, so the handler's verify-after
// re-enumeration is observing its own effect — which is what makes the
// "Repair COMPLETE" line evidence rather than an assertion about a fixed string.
func TestManageRepairEdges_ExecuteUnlinksOnlyCrossFileContains(t *testing.T) {
	callsEdge := &knowledgev1.Edge{FromId: "src/a.ts", ToId: "pkg/y.go:Callee", Type: "CALLS"}
	sameFile := repairContainsEdge("src/a.ts", "src/a.ts:Bar")
	fossil := repairContainsEdge("src/a.ts", "pkg/x.go:Foo")
	f := newRepairEdgesFake()
	baseKey := repairEdgesLayerKey{Repo: "myrepo"}
	base := f.layer(baseKey)
	base.files = []*knowledgev1.Node{repairFileNode("src/a.ts")}
	base.symbols = map[string]*knowledgev1.Node{
		"pkg/x.go:Foo":    repairSymbolNode("pkg/x.go:Foo", "pkg/x.go"),
		"src/a.ts:Bar":    repairSymbolNode("src/a.ts:Bar", "src/a.ts"),
		"pkg/y.go:Callee": repairSymbolNode("pkg/y.go:Callee", "pkg/y.go"),
	}
	base.edges = []*knowledgev1.Edge{fossil, sameFile, callsEdge}

	handled, res := repairEdgesCall(t, f,
		`{"operation":"repair_edges","graph":"code","name":"myrepo","execute":true}`)
	require.True(t, handled)
	require.False(t, res.IsError, "repair_edges execute: %s", toolResultText(res))

	require.Len(t, f.mutations, 1, "one unlink per DISTINCT TARGET — one fossil target here")
	m := f.mutations[0]
	assert.Equal(t, knowledgev1.MutationPlan_MUTATION_KIND_UNLINK, m.GetKind(),
		"the removal rides the existing UNLINK mutation")
	assert.Equal(t, string(kgtypes.EdgeContains), m.GetEdgeSpec().GetRelationship(),
		"the edge specification names CONTAINS")
	assert.Equal(t, "pkg/x.go:Foo", m.GetEdgeSpec().GetToId(), "…at the fossil's target")
	assert.True(t, m.GetEdgeSpec().GetForward(), "forward: each selected file is the edge SOURCE")
	assert.Equal(t, []string{"src/a.ts"}, m.GetSelection().GetIds(),
		"the selected set is the fossil's source file ids")

	baseEdges := f.layer(baseKey).edges
	remaining := make([]string, 0, len(baseEdges))
	for _, e := range baseEdges {
		remaining = append(remaining, e.GetFromId()+"->"+e.GetToId()+" ("+e.GetType()+")")
	}
	assert.ElementsMatch(t, []string{
		"src/a.ts->src/a.ts:Bar (CONTAINS)",
		"src/a.ts->pkg/y.go:Callee (CALLS)",
	}, remaining, "exactly the fossil was removed; the same-file CONTAINS and the cross-file CALLS survive")

	body := toolResultText(res)
	assert.Contains(t, body, "Unlinked 1 CONTAINS edge(s) across 1 graph(s).")
	assert.Contains(t, body, "code/myrepo: 0 fossil(s) remaining after the repair",
		"the completion claim is a re-enumeration, not 'the listed edges were unlinked'")
	assert.Contains(t, body, "Repair COMPLETE")
	assert.NotContains(t, body, "PARTIAL SUCCESS")
	assert.Contains(t, body, "EXECUTE against the LOCAL backend",
		"the report names the backend it acted against")
}

// TestManageRepairEdges_CloudRefusesEmptyName drives the refusal path directly:
// cloud backend, empty name, execute=true must refuse and mutate nothing. The
// second half is the known-positive control — the SAME cloud deps with the repo
// NAMED must still execute — so the test cannot pass by refusing everything.
func TestManageRepairEdges_CloudRefusesEmptyName(t *testing.T) {
	seed := func() *repairEdgesFake {
		f := newRepairEdgesFake()
		base := f.layer(repairEdgesLayerKey{Repo: "myrepo"})
		base.files = []*knowledgev1.Node{repairFileNode("src/a.ts")}
		base.symbols = map[string]*knowledgev1.Node{"pkg/x.go:Foo": repairSymbolNode("pkg/x.go:Foo", "pkg/x.go")}
		base.edges = []*knowledgev1.Edge{repairContainsEdge("src/a.ts", "pkg/x.go:Foo")}
		return f
	}

	refused := seed()
	deps := repairEdgesCloudDeps{interceptTestDeps: interceptTestDeps{gc: refused}, host: "cloud.example"}
	handled, res := InterceptManage(opCtx(), deps, kgtools.CallToolParams{
		Name: "manage", Arguments: json.RawMessage(`{"operation":"repair_edges","graph":"code","execute":true}`)})
	require.True(t, handled)
	assert.True(t, res.IsError, "an empty-name cloud sweep is refused")
	assert.Contains(t, toolResultText(res), "REFUSING an empty-name sweep against the CLOUD backend (cloud.example)")
	assert.Empty(t, refused.mutations, "the refusal mutates nothing")
	assert.Empty(t, refused.queryPlans, "the refusal short-circuits BEFORE any enumeration read")

	allowed := seed()
	depsNamed := repairEdgesCloudDeps{interceptTestDeps: interceptTestDeps{gc: allowed}, host: "cloud.example"}
	handled, res = InterceptManage(opCtx(), depsNamed, kgtools.CallToolParams{
		Name: "manage", Arguments: json.RawMessage(`{"operation":"repair_edges","graph":"code","name":"myrepo","execute":true}`)})
	require.True(t, handled)
	require.False(t, res.IsError, "a NAMED cloud repair stays allowed: %s", toolResultText(res))
	assert.Len(t, allowed.mutations, 1, "the named cloud repair really executes — the refusal is scoped to the empty name")
	assert.Contains(t, toolResultText(res), "EXECUTE against the CLOUD backend (cloud.example)")
}
