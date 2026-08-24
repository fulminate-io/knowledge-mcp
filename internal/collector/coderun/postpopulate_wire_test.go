// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// fakeGraphCaller is a package-local postpopulate.GraphCaller for the codesync
// step-linker tests. It resolves code reads by Target.Repo (a miss when Repo is
// empty) and records every create_batch mutation so a test can assert the edge
// write routes into the per-repo code graph by Target.Repo==graphName (NOT Name,
// NOT Account). It is wire-only — no store engine: reads return the typed Nodes
// carrier the client decode layer expects.
type fakeGraphCaller struct {
	// nodesByRepo[repo] = the nodes present in that code graph.
	nodesByRepo map[string][]*knowledgev1.Node

	mutations []*knowledgev1.ExecuteRequest

	// browsedTypes records the selected node type of every BROWSE page served, so
	// a test can observe which drains a caller actually performed. Enumerations
	// (RETURN_MODE_GRAPH_NAMES) are deliberately NOT recorded here: an enumeration
	// is not a browse, and conflating the two changes what the assertion means.
	browsedTypes []string
}

func (f *fakeGraphCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	if m := req.GetMutation(); m != nil {
		_ = m
		f.mutations = append(f.mutations, req)
		return &knowledgev1.ExecuteResponse{}, nil
	}
	q := req.GetQuery()
	if q.GetReturnMode() == knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES {
		return f.graphNames(), nil
	}
	return f.browse(req.GetTarget(), q), nil
}

func (f *fakeGraphCaller) graphNames() *knowledgev1.ExecuteResponse {
	var infos []*knowledgev1.GraphInfo
	for repo := range f.nodesByRepo {
		infos = append(infos, &knowledgev1.GraphInfo{Name: repo})
	}
	return &knowledgev1.ExecuteResponse{GraphNames: infos}
}

// browse serves a type-browse keyed by Target.Repo. An empty repo is a miss —
// the code graph requires a repo selector, so a name:-routed read would land in
// the wrong (or no) graph.
//
// It models the server's page semantics closely enough that the difference
// between a paged read and a capped one is observable: a fake that returns every
// match verbatim is green whether or not the read is capped, which makes any
// "the step linker saw every node" assertion vacuous.
func (f *fakeGraphCaller) browse(tgt *knowledgev1.GraphSelector, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
	f.browsedTypes = append(f.browsedTypes, q.GetSelection().GetNodeType())
	repo := tgt.GetRepo()
	if repo == "" {
		return &knowledgev1.ExecuteResponse{} // miss on empty repo.
	}
	wantType := q.GetSelection().GetNodeType()
	var matched []*knowledgev1.Node
	for _, n := range f.nodesByRepo[repo] {
		if wantType != "" && n.Type != wantType {
			continue // singular type — an INDEX selection, applied before the cap
		}
		matched = append(matched, n)
	}

	// The keyset cursor: ids strictly greater than the cursor, ascending. Only
	// applied when after_id is PRESENT, because page 1 of a drain carries a SET
	// BUT EMPTY cursor; absent, the seeded order is served so the pre-existing
	// tests keep their current meaning.
	if q.AfterId != nil {
		sort.Slice(matched, func(i, j int) bool { return matched[i].GetId() < matched[j].GetId() })
		if cursor := q.GetAfterId(); cursor != "" {
			kept := matched[:0]
			for _, n := range matched {
				if n.GetId() > cursor {
					kept = append(kept, n)
				}
			}
			matched = kept
		}
	}

	if lim := int(q.GetLimit()); lim > 0 && len(matched) > lim {
		matched = matched[:lim]
	}

	return enginetest.ResponseWithNodes(matched...)
}

func (f *fakeGraphCaller) createBatchMutations() []*knowledgev1.ExecuteRequest {
	var out []*knowledgev1.ExecuteRequest
	for _, req := range f.mutations {
		if req.GetMutation().GetKind() == knowledgev1.MutationPlan_MUTATION_KIND_CREATE {
			out = append(out, req)
		}
	}
	return out
}

// edgeKeys returns "from->to:type" for every edge across all create_batch
// mutations.
func (f *fakeGraphCaller) edgeKeys() map[string]bool {
	keys := map[string]bool{}
	for _, req := range f.createBatchMutations() {
		for _, e := range req.GetMutation().GetEdges() {
			keys[e.GetFromId()+"->"+e.GetToId()+":"+e.GetType()] = true
		}
	}
	return keys
}

// fileNode builds a NodeFile for the given repo-relative path.
func fileNode(path string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: path, Type: string(kgtypes.NodeFile)}
}

// TestLinkStepsToCode_RoutesByRepo drives LinkStepsToCode: a NodeStep whose
// file_paths metadata names an existing file node gets a KGImplements edge,
// written edges-only into the code graph by Target.Repo.
func TestLinkStepsToCode_RoutesByRepo(t *testing.T) {
	const repo = "myrepo"

	step := &knowledgev1.Node{Id: "step-1", Type: string(kgtypes.NodeStep)}
	kgtypes.SetValue(step, "file_paths", "pkg/a/x.go, pkg/missing/none.go")

	seed := []*knowledgev1.Node{
		step,
		fileNode("pkg/a/x.go"),
	}
	fc := &fakeGraphCaller{nodesByRepo: map[string][]*knowledgev1.Node{
		repo: seed,
	}}

	require.NoError(t, LinkStepsToCode(context.Background(), fc, repo))

	muts := fc.createBatchMutations()
	require.NotEmpty(t, muts, "expected one KGImplements edge write")
	for _, req := range muts {
		tgt := req.GetTarget()
		assert.Equal(t, "code", tgt.GetGraph())
		assert.Equal(t, repo, tgt.GetRepo(), "step→code edges must route by Repo==%s", repo)
		assert.Empty(t, tgt.GetName())
	}

	keys := fc.edgeKeys()
	assert.True(t, keys["step-1->pkg/a/x.go:implements"],
		"step must implement the existing file node")
	assert.False(t, keys["step-1->pkg/missing/none.go:implements"],
		"step must NOT link to a non-existent file node")
}

// TestLinkSteps_NoStepsSkipsDrain asserts the shape the early return exists to
// produce: a code graph with no step nodes is browsed for steps and then left
// alone — no second drain of every file node in the graph. That second drain is
// pure dead work, because with no steps the derived edge set is necessarily
// empty.
func TestLinkSteps_NoStepsSkipsDrain(t *testing.T) {
	const repo = "myrepo"
	fc := &fakeGraphCaller{nodesByRepo: map[string][]*knowledgev1.Node{
		repo: {fileNode("a.go")},
	}}

	require.NoError(t, LinkStepsToCode(context.Background(), fc, repo))

	assert.Contains(t, fc.browsedTypes, string(kgtypes.NodeStep),
		"the step browse is the read that decides there is nothing to link — it must still happen")
	assert.NotContains(t, fc.browsedTypes, string(kgtypes.NodeFile),
		"with no steps there is nothing to link files to, so the file drain must not run")
}

// TestLinkSteps_WithStepsDrains is the KNOWN-POSITIVE CONTROL for the recorder
// itself: it is a CHARACTERIZATION GUARD, green both before and after the early
// return. Without it, "no file drain" above is equally satisfied by a recorder
// that never records anything.
func TestLinkSteps_WithStepsDrains(t *testing.T) {
	const repo = "myrepo"

	step := &knowledgev1.Node{Id: "step-1", Type: string(kgtypes.NodeStep)}
	kgtypes.SetValue(step, "file_paths", "pkg/a/x.go")

	fc := &fakeGraphCaller{nodesByRepo: map[string][]*knowledgev1.Node{
		repo: {step, fileNode("pkg/a/x.go")},
	}}

	require.NoError(t, LinkStepsToCode(context.Background(), fc, repo))

	assert.Contains(t, fc.browsedTypes, string(kgtypes.NodeFile),
		"with steps present the file drain must run — this is what proves the recorder can see it at all")
}

// TestLinkStepsToCode_NoSteps_NoWrite confirms a code graph with no steps fires
// no create_batch mutation (the empty-edges no-op).
func TestLinkStepsToCode_NoSteps_NoWrite(t *testing.T) {
	const repo = "myrepo"
	fc := &fakeGraphCaller{nodesByRepo: map[string][]*knowledgev1.Node{
		repo: {fileNode("a.go")},
	}}
	require.NoError(t, LinkStepsToCode(context.Background(), fc, repo))
	assert.Empty(t, fc.createBatchMutations(), "no steps → no edge write")
}
