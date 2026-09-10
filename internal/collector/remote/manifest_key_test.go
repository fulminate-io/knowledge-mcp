// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// manifest_key_test.go — the DIFF KEY's own unit tests: which family diffs on
// which unit, how an entry's key is read through its arm, what a served manifest
// must satisfy over those keys, and the two pieces of arithmetic whose answers
// differ per unit.
//
// THE END-TO-END ARM IS sink_custom_diff_test.go, which drives the real
// UploadSink and asserts on wire bytes. These are the pieces; that is the whole.

// customType is the registered graph type every custom fixture in this package
// uses. It is a LITERAL rather than a name resolved from a registry: the sink
// recognizes a registered family by EXCLUSION from the built-ins, so a fixture
// that reached for a registry would be testing the registry.
const customType kgtypes.GraphType = "jira"

// TestDiffKeyKindFor_FamilyTable pins the family gate, both halves of it: which
// families diff at all, and on which unit.
//
// IT REPLACES TestDiffEligibleGraph_OnlyCodeIsEligible, which pinned the same
// answer for a gate that had only one eligible family and could therefore assert
// eligibility alone. This is that table with the second question added and the
// registered-custom rows filled in; every family the old test named is still
// asserted here, and the last line ties the boolean predicate to the kind so the
// two can never disagree.
//
// THE WEB ROW IS THE ONE THAT MATTERS MOST and it is a NEGATIVE: a budget-bounded
// re-crawl legitimately re-materializes only a subset of its previous paths, and
// every deletion guard would admit that subset as deliberate. It is asserted
// beside the custom row precisely because this change admits a new family — an
// admission that widened to web would arm a destructive path nothing else
// catches.
func TestDiffKeyKindFor_FamilyTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		gt   kgtypes.GraphType
		want diffKeyKind
	}{
		{"code diffs per file", kgtypes.GraphCode, diffKeyFile},
		{"a registered custom type diffs per node", customType, diffKeyNode},
		{"another registered custom type diffs per node", kgtypes.GraphType("notion-workspace"), diffKeyNode},
		{"web never diffs", kgtypes.GraphWebRaw, diffKeyNone},
		{"pdf never diffs", kgtypes.GraphPDFRaw, diffKeyNone},
		{"knowledge is user-curated and never diffs", kgtypes.GraphKnowledge, diffKeyNone},
		{"practice is user-curated and never diffs", kgtypes.GraphPractice, diffKeyNone},
		{"checks is user-curated and never diffs", kgtypes.GraphChecks, diffKeyNone},
		{"linkage is user-curated and never diffs", kgtypes.GraphLinkage, diffKeyNone},
		{"the empty type never diffs", kgtypes.GraphType(""), diffKeyNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, diffKeyKindFor(tc.gt))
			require.Equal(t, tc.want != diffKeyNone, diffEligibleGraph(tc.gt),
				"diffEligibleGraph must be exactly 'the kind is not none' — two predicates that can disagree is two gates")
		})
	}
}

// entryFor is a fixture builder for one served entry.
func entryFor(kind diffKeyKind, key string, fill byte) *knowledgev1.ManifestEntry {
	return newManifestEntry(kind, key, bytes.Repeat([]byte{fill}, contribHashBytes))
}

// TestManifestEntryKey_ReadsTheArm covers the accessor every consumer routes
// through, including the shape that motivates its existence: a node-keyed entry
// whose file arm reads as the empty string.
func TestManifestEntryKey_ReadsTheArm(t *testing.T) {
	t.Run("file arm", func(t *testing.T) {
		key, kind, ok := manifestEntryKey(entryFor(diffKeyFile, "pkg/a.go", 1))
		require.True(t, ok)
		require.Equal(t, diffKeyFile, kind)
		require.Equal(t, "pkg/a.go", key)
	})
	t.Run("node arm", func(t *testing.T) {
		e := entryFor(diffKeyNode, "issue-1", 1)
		key, kind, ok := manifestEntryKey(e)
		require.True(t, ok)
		require.Equal(t, diffKeyNode, kind)
		require.Equal(t, "issue-1", key)
		require.Empty(t, e.GetFilePath(),
			"THE HAZARD, pinned: the file accessor on a node-keyed entry returns the empty string rather than failing")
	})
	t.Run("no arm set at all", func(t *testing.T) {
		_, _, ok := manifestEntryKey(&knowledgev1.ManifestEntry{ContributionHash: bytes.Repeat([]byte{1}, 32)})
		require.False(t, ok, "an entry with no key arm carries no key")
	})
	t.Run("an arm set to the empty string", func(t *testing.T) {
		_, _, ok := manifestEntryKey(newManifestEntry(diffKeyNode, "", nil))
		require.False(t, ok, "an empty key is no key: every entry carrying one would collide")
	})
}

// TestManifestSelfConsistent_ArmsAndDefects drives the served-manifest contract
// over both units and every way it can break. A violation ABORTS the collect, so
// each row also asserts the defect message names the rule and the entry.
func TestManifestSelfConsistent_ArmsAndDefects(t *testing.T) {
	full := bytes.Repeat([]byte{9}, contribHashBytes)
	for _, tc := range []struct {
		name        string
		kind        diffKeyKind
		entries     []*knowledgev1.ManifestEntry
		wantOK      bool
		defectNames []string
	}{
		{
			name: "a node-keyed manifest satisfies a node-keyed collect",
			kind: diffKeyNode,
			entries: []*knowledgev1.ManifestEntry{
				entryFor(diffKeyNode, "issue-1", 1), entryFor(diffKeyNode, "issue-2", 2),
			},
			wantOK: true,
		},
		{
			name:    "a file-keyed manifest satisfies a file-keyed collect",
			kind:    diffKeyFile,
			entries: []*knowledgev1.ManifestEntry{entryFor(diffKeyFile, "pkg/a.go", 1)},
			wantOK:  true,
		},
		{
			name:    "an empty entry list is consistent under either unit",
			kind:    diffKeyNode,
			entries: nil,
			wantOK:  true,
		},
		{
			// THE CROSS-ARM ROW IS THE LOAD-BEARING ONE. A manifest keyed on the other
			// unit describes different rows than the ones this collect hashed, so every
			// comparison against it is meaningless and the deletion set it produces
			// names the whole graph.
			name:        "a file-keyed manifest is REFUSED for a node-keyed collect",
			kind:        diffKeyNode,
			entries:     []*knowledgev1.ManifestEntry{entryFor(diffKeyFile, "pkg/a.go", 1)},
			wantOK:      false,
			defectNames: []string{"file-keyed", "node"},
		},
		{
			name:        "a node-keyed manifest is REFUSED for a file-keyed collect",
			kind:        diffKeyFile,
			entries:     []*knowledgev1.ManifestEntry{entryFor(diffKeyNode, "issue-1", 1)},
			wantOK:      false,
			defectNames: []string{"node-keyed", "file"},
		},
		{
			name:        "an entry with no arm at all is REFUSED",
			kind:        diffKeyNode,
			entries:     []*knowledgev1.ManifestEntry{{ContributionHash: full}},
			wantOK:      false,
			defectNames: []string{"EMPTY or UNSET key"},
		},
		{
			name:        "a node id repeated is REFUSED",
			kind:        diffKeyNode,
			entries:     []*knowledgev1.ManifestEntry{entryFor(diffKeyNode, "issue-1", 1), entryFor(diffKeyNode, "issue-1", 2)},
			wantOK:      false,
			defectNames: []string{"issue-1", "more than once"},
		},
		{
			name:        "a short hash is REFUSED",
			kind:        diffKeyNode,
			entries:     []*knowledgev1.ManifestEntry{newManifestEntry(diffKeyNode, "issue-1", []byte{1, 2, 3})},
			wantOK:      false,
			defectNames: []string{"issue-1", "3-byte contribution hash"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &knowledgev1.CollectManifestResponse{ManifestId: "m", Entries: tc.entries}
			require.Equal(t, tc.wantOK, manifestSelfConsistent(resp, tc.kind))
			defect := manifestDefect(resp, tc.kind)
			if tc.wantOK {
				require.Empty(t, defect, "a consistent manifest names no defect")
				return
			}
			require.NotEmpty(t, defect, "an inconsistent manifest must name WHICH rule it broke")
			for _, want := range tc.defectNames {
				require.Contains(t, defect, want)
			}
		})
	}
}

// TestComputeCollectDiff_NodeKeyed covers the comparison's input classes over
// the node unit: an empty manifest, an empty present set, identity, disjoint
// sets, and one key present on both sides with differing hashes.
//
// EVERY ROW ASSERTS THE EXACT SETS rather than their sizes. A count of one is
// satisfied by classifying the WRONG key.
func TestComputeCollectDiff_NodeKeyed(t *testing.T) {
	h := func(fill byte) [32]byte {
		var out [32]byte
		for i := range out {
			out[i] = fill
		}
		return out
	}
	manifest := func(entries ...*knowledgev1.ManifestEntry) *knowledgev1.CollectManifestResponse {
		return &knowledgev1.CollectManifestResponse{ManifestId: "m", Entries: entries}
	}
	for _, tc := range []struct {
		name          string
		resp          *knowledgev1.CollectManifestResponse
		present       map[string][32]byte
		wantChanged   []string
		wantUnchanged []string
	}{
		{
			name:        "an empty server manifest makes every present key changed",
			resp:        manifest(),
			present:     map[string][32]byte{"issue-1": h(1), "issue-2": h(2)},
			wantChanged: []string{"issue-1", "issue-2"},
		},
		{
			name:    "a non-empty manifest against an empty present set classifies nothing",
			resp:    manifest(entryFor(diffKeyNode, "issue-1", 1)),
			present: map[string][32]byte{},
		},
		{
			name:          "identical sets are all unchanged",
			resp:          manifest(entryFor(diffKeyNode, "issue-1", 1), entryFor(diffKeyNode, "issue-2", 2)),
			present:       map[string][32]byte{"issue-1": h(1), "issue-2": h(2)},
			wantUnchanged: []string{"issue-1", "issue-2"},
		},
		{
			name:        "disjoint sets leave every present key changed",
			resp:        manifest(entryFor(diffKeyNode, "gone-1", 1)),
			present:     map[string][32]byte{"issue-9": h(9)},
			wantChanged: []string{"issue-9"},
		},
		{
			name:          "one key on both sides with a differing hash is changed and the other is not",
			resp:          manifest(entryFor(diffKeyNode, "issue-1", 1), entryFor(diffKeyNode, "issue-2", 2)),
			present:       map[string][32]byte{"issue-1": h(0xEE), "issue-2": h(2)},
			wantChanged:   []string{"issue-1"},
			wantUnchanged: []string{"issue-2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := computeCollectDiff(tc.resp, tc.present, diffKeyNode)
			require.Equal(t, diffKeyNode, d.kind)
			require.Equal(t, tc.wantChanged, d.changedKeys)
			require.Equal(t, tc.wantUnchanged, d.unchangedKeys)
		})
	}
}

// TestDeletionSet_NodeKeyedNamesNoDirectories is the arithmetic's per-unit
// difference, and the directory assertion is the load-bearing half.
//
// UNDER THE FILE KEY the same manifest yields the missing path PLUS its ancestor
// directories, because a package node has no file of its own and naming its
// directory is the only way to delete it. UNDER THE NODE KEY those ids name no
// row at all, every one of them would fail the server's per-entry validation, and
// a single failure refuses the WHOLE deletion set — so a node-keyed collect that
// emitted them could never delete anything.
func TestDeletionSet_NodeKeyedNamesNoDirectories(t *testing.T) {
	// THE FIXTURE HAS TO EMPTY A DIRECTORY, or the file arm names no directory
	// either and the two units answer alike for a reason that has nothing to do
	// with the key. Here a/b loses its only file while x/ keeps one.
	manifest := map[string][32]byte{"a/b/c.go": {1}, "x/y.go": {2}}

	fileKeyed := deletionSet(manifest, []string{"x/y.go"}, nil, diffKeyFile)
	require.Contains(t, fileKeyed, "a/b/c.go", "the unhandled file is named")
	require.Contains(t, fileKeyed, "a/b",
		"and so is the directory it emptied — a package node has no file of its own")
	require.NotContains(t, fileKeyed, "x", "a directory with a surviving file beneath it is NOT named")

	nodeKeyed := deletionSet(manifest, []string{"x/y.go"}, nil, diffKeyNode)
	require.Equal(t, []string{"a/b/c.go"}, nodeKeyed,
		"under the node key the set is EXACTLY the unhandled keys — no ancestor ids are invented, "+
			"and every invented one would fail the server's per-entry validation and refuse the WHOLE set")
}

// TestFilterToChangedRows_NodeKeyed covers the upload filter's node predicate
// and the orphan-edge rule that has no file-keyed analog.
func TestFilterToChangedRows_NodeKeyed(t *testing.T) {
	nodes := []*knowledgev1.Node{
		{Id: "issue-1", Type: "issue"},
		{Id: "issue-2", Type: "issue"},
	}
	t.Run("a node is kept on its OWN id, not on a file path it does not have", func(t *testing.T) {
		kept, _, _, err := filterToChangedRows(nodes, nil, nil, []string{"issue-2"}, false, diffKeyNode)
		require.NoError(t, err)
		require.Len(t, kept, 1)
		require.Equal(t, "issue-2", kept[0].GetId(),
			"the changed set named issue-2, so issue-2 and nothing else goes on the wire")
	})
	t.Run("an edge rides with its FROM node and is dropped when that node is not kept", func(t *testing.T) {
		edges := []kgwire.BatchEdge{
			{FromIdx: -1, ToIdx: -1, FromID: "issue-2", ToID: "issue-1", Type: kgtypes.EdgeType("BLOCKS")},
			{FromIdx: -1, ToIdx: -1, FromID: "issue-1", ToID: "issue-2", Type: kgtypes.EdgeType("BLOCKS")},
		}
		_, _, keptEdges, err := filterToChangedRows(nodes, nil, edges, []string{"issue-2"}, false, diffKeyNode)
		require.NoError(t, err)
		require.Len(t, keptEdges, 1)
		require.Equal(t, "issue-2", keptEdges[0].FromID)
	})
	t.Run("an ORPHAN edge is carried rather than dropped", func(t *testing.T) {
		// Its FROM node is in no group, so no key covers it, no manifest entry can
		// describe it and no diff can ever mark it changed. Dropping it under a diff
		// would mean it never lands again after the first collect.
		edges := []kgwire.BatchEdge{
			{FromIdx: -1, ToIdx: -1, FromID: "not-in-this-result", ToID: "issue-1", Type: kgtypes.EdgeType("BLOCKS")},
		}
		_, _, keptEdges, err := filterToChangedRows(nodes, nil, edges, nil, false, diffKeyNode)
		require.NoError(t, err)
		require.Len(t, keptEdges, 1, "an edge no key covers must ride every collect, or it can never re-land")
	})
	t.Run("an index-addressed edge is still refused", func(t *testing.T) {
		edges := []kgwire.BatchEdge{{FromIdx: 0, ToIdx: 1, Type: kgtypes.EdgeType("BLOCKS")}}
		_, _, _, err := filterToChangedRows(nodes, nil, edges, []string{"issue-1"}, false, diffKeyNode)
		require.Error(t, err, "an unplaceable edge errors under either unit")
		require.Contains(t, err.Error(), "INDEX-ADDRESSED")
	})
	t.Run("the digest array is narrowed by the same predicate", func(t *testing.T) {
		alpha := [32]byte{0xAA}
		beta := [32]byte{0xBB}
		kept, keptHashes, _, err := filterToChangedRows(
			nodes, [][32]byte{alpha, beta}, nil, []string{"issue-2"}, false, diffKeyNode)
		require.NoError(t, err)
		require.Len(t, kept, 1)
		require.Equal(t, [][32]byte{beta}, keptHashes,
			"the surviving node's OWN digest rides with it, not its predecessor's")
	})
}

// TestEntriesFor_NodeArm pins that a node-keyed chunk echoes its keys on the
// NODE arm. A renderer that spelled the file arm would ship ids in file_path,
// where the server's chunk decoder refuses the request and its snapshot decoder
// silently loses every key.
func TestEntriesFor_NodeArm(t *testing.T) {
	h := chunkHashFields{
		kind:         diffKeyNode,
		perKeyHashes: map[string][32]byte{"issue-1": {1}, "issue-2": {2}},
	}
	entries := h.entriesForNodes([]*knowledgev1.Node{{Id: "issue-2"}, {Id: "issue-1"}})
	require.Len(t, entries, 2)
	require.Equal(t, []string{"issue-1", "issue-2"},
		[]string{entries[0].GetNodeId(), entries[1].GetNodeId()},
		"entries are ordered by key so a chunk's wire bytes reproduce across runs")
	for _, e := range entries {
		key, kind, ok := manifestEntryKey(e)
		require.True(t, ok)
		require.Equal(t, diffKeyNode, kind, "a node-keyed chunk echoes on the NODE arm")
		require.NotEmpty(t, key)
		require.Empty(t, e.GetFilePath(), "and nothing lands in the file arm")
	}

	t.Run("an edge's owning key is its FROM node id", func(t *testing.T) {
		got := h.entriesForEdges([]*knowledgev1.BatchEdge{{FromId: "issue-1"}})
		require.Len(t, got, 1)
		require.Equal(t, "issue-1", got[0].GetNodeId())
	})
	t.Run("a key with no computed hash is omitted", func(t *testing.T) {
		got := h.entriesForNodes([]*knowledgev1.Node{{Id: "issue-never-hashed"}})
		require.Empty(t, got, "the server then has nothing to compare and lands the rows — the fail-closed side")
	})
	t.Run("an id-less node contributes no entry", func(t *testing.T) {
		got := h.entriesForNodes([]*knowledgev1.Node{{Type: "issue"}})
		require.Empty(t, got)
	})
}

// TestDeletionsFor_CarriersAreExclusive pins the router between the Finalize
// request's two deletion carriers: one decision produces ONE list under ONE
// unit, and asking for the other unit returns nothing.
//
// SENDING THE SAME LIST TO BOTH is the failure this guards. The server resolves
// the two fields under different rules and refuses a request carrying both, so a
// duplicated list would refuse every deletion phase on that graph.
func TestDeletionsFor_CarriersAreExclusive(t *testing.T) {
	nodeDecision := uploadDecision{kind: diffKeyNode, deletions: []string{"issue-7"}}
	require.Equal(t, []string{"issue-7"}, deletionsFor(nodeDecision, diffKeyNode))
	require.Nil(t, deletionsFor(nodeDecision, diffKeyFile),
		"a node-keyed decision puts NOTHING in the file carrier")

	fileDecision := uploadDecision{kind: diffKeyFile, deletions: []string{"pkg/gone.go"}}
	require.Equal(t, []string{"pkg/gone.go"}, deletionsFor(fileDecision, diffKeyFile))
	require.Nil(t, deletionsFor(fileDecision, diffKeyNode),
		"and a file-keyed decision puts nothing in the node carrier")
}
