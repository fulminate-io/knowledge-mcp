// SPDX-License-Identifier: Apache-2.0

package tools

// rebuild_segments_singleton_test.go covers the FOURTH seam the same empty-name
// conflation reached: manage(rebuild_segments) resolved an empty instance name to
// the canonical one for the knowledge graph ALONE.
//
// THE CONSEQUENCE WAS AN UNREACHABLE OPERATION, not a wrong result. The checks
// graph addresses no instance name, so a caller has none to give; the
// required-name check then refused every call, and the graph that most needs a
// manual rebuild — its collector had only just started running — was the one
// graph the rebuild could not be asked about.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

// TestRebuildSegments_ChecksResolvesItsCanonicalInstance drives the operation the
// way a caller must: naming the graph and nothing else.
func TestRebuildSegments_ChecksResolvesItsCanonicalInstance(t *testing.T) {
	scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{
		makeScanPage("chk", 0, searchengine.DefaultMinSegmentDocs),
	}}
	shipper := &fakeRebuildShipper{}
	deps := rebuildClientDeps{scanner: scanner, shipper: shipper}

	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
		Operation: "rebuild_segments", Graph: string(kgtypes.GraphChecks),
	})

	require.False(t, res.IsError,
		"a checks rebuild names no instance because the graph addresses none — refusing it makes the "+
			"operation unreachable for this graph: %s", engine.FirstTextContent(res))
	assert.Positive(t, scanner.calls, "the rebuild must actually scan, not return an empty success")
}

// TestRebuildSegments_StillRequiresANameWhereOneExists is the control that keeps
// the fix from swallowing a real usage error.
//
// For a family that HAS an instance field, an empty name is a caller mistake and
// must still be refused by name — canonicalization is the identity there, so the
// required-name check keeps its whole job for code, cloud, cicd and practice.
func TestRebuildSegments_StillRequiresANameWhereOneExists(t *testing.T) {
	deps := rebuildClientDeps{scanner: &fakeRebuildScanner{}, shipper: &fakeRebuildShipper{}}

	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
		Operation: "rebuild_segments", Graph: string(kgtypes.GraphCode),
	})
	require.True(t, res.IsError, "a code rebuild with no repo is a usage error and must stay one")
	assert.Contains(t, engine.FirstTextContent(res), `requires "name"`)
}

// TestRebuildSegments_KnowledgeAliasAndDefaultBothSurvive pins the knowledge
// behavior the generalization moved.
//
// The empty-name resolution used to live INSIDE the knowledge branch along with
// the type-name alias and the overlay refusal. Only the empty-name half moved
// out, so this asserts the two that stayed are still where they were — a
// refactor that carried them along would be a silent behavior change for the
// graph this whole convention came from.
func TestRebuildSegments_KnowledgeAliasAndDefaultBothSurvive(t *testing.T) {
	t.Run("the knowledge type name is still an alias for its one instance", func(t *testing.T) {
		scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{
			makeScanPage("kg", 0, searchengine.DefaultMinSegmentDocs),
		}}
		deps := rebuildClientDeps{scanner: scanner, shipper: &fakeRebuildShipper{}}
		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
			Operation: "rebuild_segments", Graph: string(kgtypes.GraphKnowledge), Name: string(kgtypes.GraphKnowledge),
		})
		require.False(t, res.IsError, "the alias must still resolve: %s", engine.FirstTextContent(res))
	})

	t.Run("an empty knowledge name still resolves to the canonical instance", func(t *testing.T) {
		scanner := &fakeRebuildScanner{pages: [][]*knowledgev1.PipelineScanItem{
			makeScanPage("kg", 0, searchengine.DefaultMinSegmentDocs),
		}}
		deps := rebuildClientDeps{scanner: scanner, shipper: &fakeRebuildShipper{}}
		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
			Operation: "rebuild_segments", Graph: string(kgtypes.GraphKnowledge),
		})
		require.False(t, res.IsError, "the empty name must still resolve: %s", engine.FirstTextContent(res))
	})

	t.Run("a knowledge overlay is still refused", func(t *testing.T) {
		deps := rebuildClientDeps{scanner: &fakeRebuildScanner{}, shipper: &fakeRebuildShipper{}}
		res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
			Operation: "rebuild_segments", Graph: string(kgtypes.GraphKnowledge), Name: "default@session",
		})
		require.True(t, res.IsError, "overlay rebuilds are not supported and the refusal must survive")
		assert.Contains(t, engine.FirstTextContent(res), "overlay")
	})
}

// TestRebuildSegments_ErrorTextNamesEveryEmbeddableBuiltin keeps the refusal
// message honest.
//
// The message enumerates the graphs that DO have rebuildable segments, and a
// caller reads it to find out what to ask for instead. It listed five while the
// gate admitted six, so the one graph a reader most needed to discover was the
// one the message said did not exist. The list is checked against the predicate
// rather than against a copy of itself.
func TestRebuildSegments_ErrorTextNamesEveryEmbeddableBuiltin(t *testing.T) {
	deps := rebuildClientDeps{scanner: &fakeRebuildScanner{}, shipper: &fakeRebuildShipper{}}
	res := handleClientRebuildSegments(context.Background(), deps, manageArgs{
		Operation: "rebuild_segments", Graph: string(kgtypes.GraphLinkage),
	})
	require.True(t, res.IsError, "linkage carries no rebuildable segments and must be refused")
	body := engine.FirstTextContent(res)

	var named int
	for _, name := range kgtypes.BuiltinGraphTypeNames() {
		if !kgtypes.HasRebuildableSegments(kgtypes.GraphType(name)) {
			continue
		}
		named++
		assert.Contains(t, body, name,
			"the refusal enumerates what a caller CAN rebuild; %q is admitted by the gate but missing from the list", name)
	}
	require.Positive(t, named,
		"control: no builtin reported rebuildable segments, so the loop asserted nothing")
}

// TestCanonicalInstanceName_IsIdentityOffTheSingletons pins the helper's blast
// radius, which is what makes it safe to apply on shared paths.
//
// It is applied inside composeSegmentGraphSearch, which serves registered custom
// graphs as well as checks, so "identity for everything else" is a property those
// callers depend on rather than a nicety. The overlay row is the one that would
// bite hardest: canonicalizing a "repo@branch" search would silently redirect it
// to the base pool.
func TestCanonicalInstanceName_IsIdentityOffTheSingletons(t *testing.T) {
	for _, tc := range []struct {
		gt       kgtypes.GraphType
		in, want string
	}{
		{kgtypes.GraphChecks, "", workingset.DefaultInstanceName},
		{kgtypes.GraphKnowledge, "", workingset.DefaultInstanceName},
		{kgtypes.GraphCode, "", ""},
		{kgtypes.GraphCode, "myrepo", "myrepo"},
		{kgtypes.GraphCode, "myrepo@branch", "myrepo@branch"},
		{kgtypes.GraphPractice, "go", "go"},
		{kgtypes.GraphType("customtype"), "instance", "instance"},
		{kgtypes.GraphType("customtype"), "", ""},
	} {
		assert.Equal(t, tc.want, workingset.CanonicalInstanceName(tc.gt, tc.in),
			"CanonicalInstanceName(%q, %q)", tc.gt, tc.in)
	}
}

// TestSegmentTargetsResolveTheCanonicalInstanceForSingletons sweeps the REMAINING
// (graph type, name)-keyed seams a caller can reach with a checks selector.
//
// THEY ARE GROUPED BECAUSE THEY SHARE ONE CAUSE, and each produced a different
// wrong key from the same empty input — one an empty name, the other the literal
// graph-type name. Both silently addressed an instance nothing had written to:
// the cache drop removed nothing, and the delete left a removed check's segment
// entry searchable. Neither reported anything.
func TestSegmentTargetsResolveTheCanonicalInstanceForSingletons(t *testing.T) {
	canonical := workingset.CanonicalInstanceName(kgtypes.GraphChecks, "")
	require.NotEmpty(t, canonical, "control: the checks graph must have a canonical instance")

	t.Run("the drop_graph cache target keys the canonical instance", func(t *testing.T) {
		gt, name := dropGraphCacheTarget(manageArgs{Graph: string(kgtypes.GraphChecks)})
		assert.Equal(t, kgtypes.GraphChecks, gt)
		assert.Equal(t, canonical, name,
			"dropping the checks cache under an empty key removes nothing — the cache is under the canonical instance")
	})

	t.Run("the delete segment target keys the canonical instance", func(t *testing.T) {
		gt, name := deleteSegmentTarget(string(kgtypes.GraphChecks), "")
		assert.Equal(t, kgtypes.GraphChecks, gt)
		assert.Equal(t, canonical, name,
			"the graph-type-name fallback keys a third spelling nothing wrote to, so a deleted check stays searchable")
		assert.NotEqual(t, string(kgtypes.GraphChecks), name,
			"the literal graph-type name is the fallback this seam must no longer take for a singleton")
	})

	// CONTROLS — every family that carries a real instance field keeps its exact
	// prior behavior at both seams, including the fallbacks.
	t.Run("a named instance is untouched at both seams", func(t *testing.T) {
		gt, name := dropGraphCacheTarget(manageArgs{Graph: string(kgtypes.GraphCode), Name: "myrepo"})
		assert.Equal(t, kgtypes.GraphCode, gt)
		assert.Equal(t, "myrepo", name)

		gt, name = deleteSegmentTarget(string(kgtypes.GraphCode), "myrepo")
		assert.Equal(t, kgtypes.GraphCode, gt)
		assert.Equal(t, "myrepo", name)
	})

	t.Run("the code graph-type-name fallback survives", func(t *testing.T) {
		// code carries a real instance field, so an absent one is a caller
		// mistake and this seam's long-standing fallback still applies.
		gt, name := deleteSegmentTarget(string(kgtypes.GraphCode), "")
		assert.Equal(t, kgtypes.GraphCode, gt)
		assert.Equal(t, string(kgtypes.GraphCode), name,
			"canonicalization is the identity for a family with an instance field, so the fallback is unchanged")
	})

	t.Run("knowledge keeps its own pinning at both seams", func(t *testing.T) {
		gt, name := deleteSegmentTarget(string(kgtypes.GraphKnowledge), "anything")
		assert.Equal(t, kgtypes.GraphKnowledge, gt)
		assert.Equal(t, knowledgeDefaultName, name,
			"the knowledge seam pins its one instance regardless of the name given, and that is unchanged")

		gt, name = dropGraphCacheTarget(manageArgs{Graph: string(kgtypes.GraphKnowledge)})
		assert.Equal(t, kgtypes.GraphKnowledge, gt)
		assert.Equal(t, workingset.DefaultInstanceName, name)

		gt, name = dropGraphCacheTarget(manageArgs{
			Graph: string(kgtypes.GraphKnowledge), Name: string(kgtypes.GraphKnowledge)})
		assert.Equal(t, kgtypes.GraphKnowledge, gt)
		assert.Equal(t, workingset.DefaultInstanceName, name,
			"the knowledge type name is still an alias for its one instance")
	})
}
