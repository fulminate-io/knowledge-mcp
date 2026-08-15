// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// fakeGraphCaller is a package-local postpopulate.GraphCaller for the codesync
// hierarchy / step-linker tests. It resolves code reads by Target.Repo (a miss
// when Repo is empty) and records every create_batch mutation so a test can
// assert the package-node + edge write routes into the per-repo code graph by
// Target.Repo==graphName (NOT Name, NOT Account). It is wire-only — no store
// engine: reads return the typed Nodes carrier the client decode layer expects.
type fakeGraphCaller struct {
	// nodesByRepo[repo] = the nodes present in that code graph.
	nodesByRepo map[string][]*knowledgev1.Node

	mutations []*knowledgev1.ExecuteRequest
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
// "the hierarchy saw all the files" assertion vacuous.
func (f *fakeGraphCaller) browse(tgt *knowledgev1.GraphSelector, q *knowledgev1.QueryPlan) *knowledgev1.ExecuteResponse {
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

// nodeIDs returns the set of node ids materialized across all create_batch
// mutations.
func (f *fakeGraphCaller) nodeIDs() map[string]bool {
	ids := map[string]bool{}
	for _, req := range f.createBatchMutations() {
		for _, n := range req.GetMutation().GetNodeBodies() {
			ids[n.GetId()] = true
		}
	}
	return ids
}

// fileNode builds a NodeFile for the given repo-relative path.
func fileNode(path string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: path, Type: string(kgtypes.NodeFile)}
}

// TestBuildHierarchy_RoutesByRepo drives the real BuildHierarchy through the wire
// fake: package nodes + the dir→file / dir hierarchy edges must be emitted in a
// create_batch whose Target.Repo == the repo graph name (NOT Name, NOT Account)
// — the selector-field invariant for the code graph. File→chunk CONTAINS
// edges are NOT built here (the treesitter chunker emits them at collect time).
func TestBuildHierarchy_RoutesByRepo(t *testing.T) {
	const repo = "myrepo"

	seed := []*knowledgev1.Node{
		fileNode("pkg/a/x.go"),
		fileNode("pkg/a/y.go"),
		fileNode("pkg/b/z.go"),
	}

	fc := &fakeGraphCaller{nodesByRepo: map[string][]*knowledgev1.Node{
		repo: seed,
	}}

	require.NoError(t, BuildHierarchy(context.Background(), fc, repo))

	muts := fc.createBatchMutations()
	require.NotEmpty(t, muts, "expected a create_batch with package nodes + edges")
	for _, req := range muts {
		tgt := req.GetTarget()
		assert.Equal(t, "code", tgt.GetGraph(), "hierarchy write must target the code graph")
		assert.Equal(t, repo, tgt.GetRepo(), "code write must route by Repo==%s (NOT Name)", repo)
		assert.Empty(t, tgt.GetName(), "code write must NOT route by Name")
		assert.Empty(t, tgt.GetAccount(), "code write must NOT route by Account")
	}

	// Package nodes materialized for both directories + repo root.
	ids := fc.nodeIDs()
	assert.True(t, ids["pkg/a"], "package node pkg/a must be created")
	assert.True(t, ids["pkg/b"], "package node pkg/b must be created")
	assert.True(t, ids["pkg"], "intermediate package node pkg must be created")
	assert.True(t, ids["."], "repo root package node must be created")

	// Edges: dir→file, parent→child, root→top.
	keys := fc.edgeKeys()
	assert.True(t, keys["pkg/a->pkg/a/x.go:CONTAINS"], "pkg/a must contain x.go")
	assert.True(t, keys["pkg->pkg/a:CONTAINS"], "pkg must contain pkg/a")
	assert.True(t, keys[".->pkg:CONTAINS"], "root must contain pkg")
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

// countFileContainsEdges counts the CONTAINS edges whose target is one of the
// seeded file ids — the dir→file edges. The dir→dir hierarchy edges carry the
// same type, so membership in the seeded set is what separates them.
func countFileContainsEdges(fc *fakeGraphCaller, seeded map[string]bool) int {
	n := 0
	for _, req := range fc.createBatchMutations() {
		for _, e := range req.GetMutation().GetEdges() {
			if e.GetType() == string(kgtypes.EdgeContains) && seeded[e.GetToId()] {
				n++
			}
		}
	}
	return n
}

// TestBuildHierarchy_DrainsAllPages seeds more than two full browse pages of file
// nodes and asserts every one of them ends up with a dir→file CONTAINS edge. The
// file count is written as the page-size expression rather than a literal so the
// test tracks the page size, and three pages (two full plus a short one) means
// the short-final-page termination is exercised rather than assumed.
func TestBuildHierarchy_DrainsAllPages(t *testing.T) {
	const repo = "myrepo"

	wantFiles := paging.BrowsePageSize*2 + 7

	seed := make([]*knowledgev1.Node, 0, wantFiles)
	seeded := make(map[string]bool, wantFiles)
	for i := range wantFiles {
		id := fmt.Sprintf("pkg/d%03d/f%04d.go", i%50, i)
		seed = append(seed, fileNode(id))
		seeded[id] = true
	}
	// Fixture-derived cardinality guard: a fixture that silently built the wrong
	// number of nodes must not be able to satisfy the assertion below by
	// shrinking both sides of it.
	require.Len(t, seed, wantFiles)
	require.Len(t, seeded, wantFiles)

	fc := &fakeGraphCaller{nodesByRepo: map[string][]*knowledgev1.Node{
		repo: seed,
	}}

	require.NoError(t, BuildHierarchy(context.Background(), fc, repo))

	got := countFileContainsEdges(fc, seeded)
	assert.Equal(t, wantFiles, got,
		"hierarchy must contain every file node: want %d, got %d", wantFiles, got)
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
