// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// The two-pool fixture vocabulary. rareTerm is the query's TRUE match: it occurs
// in exactly one BASE document and in EVERY (much smaller) overlay document, so
// a merge that compares raw scores ranks the base document first while a merge
// that only interleaves ranked lists hands the overlay half the result set.
// sharedTerm exists so the SHARED document id lands in BOTH pools' hit lists —
// without it the base copy never enters the base hit list and the dedup
// assertion would be satisfied by an uncontested id.
const (
	overlayRareTerm   = "zzqqxxuniquetarget"
	overlaySharedTerm = "yyppwwsharedmarker"
	overlayFiller     = "shared corpus filler body common token"

	overlayBaseGraph    = "repo"
	overlayBranchGraph  = "repo@branch"
	overlayTargetID     = "n7"
	overlaySharedID     = "n500"
	overlayBranchDocN   = 16
	overlayBranchPrefix = "b"
)

// overlayVec builds a deterministic 32-byte vector for doc i of a named corpus,
// so a fixture is reproducible across runs (the searchCorpus idiom).
func overlayVec(seed uint64, i int) []byte {
	rng := rand.New(rand.NewPCG(seed, uint64(i)+1))
	v := make([]byte, 32)
	for j := range v {
		v[j] = byte(rng.UintN(256))
	}
	return v
}

// overlayBaseDocs builds the BASE pool corpus: searchCorpusN documents in which
// overlayRareTerm occurs in EXACTLY ONE document (the target, which carries it
// three times so its BM25 score clears the shared document's within the same
// pool) and overlaySharedTerm occurs in EXACTLY ONE document (the shared id).
func overlayBaseDocs() []searchengine.Document {
	docs := make([]searchengine.Document, searchCorpusN)
	for i := range docs {
		id := fmt.Sprintf("n%d", i)
		summary := overlayFiller
		switch id {
		case overlayTargetID:
			summary = strings.Join([]string{overlayRareTerm, overlayRareTerm, overlayRareTerm, overlayFiller}, " ")
		case overlaySharedID:
			summary = overlaySharedTerm + " " + overlayFiller
		}
		docs[i] = searchengine.Document{
			ID:     id,
			Vector: overlayVec(0x5EED, i),
			Fields: map[string]string{searchengine.FieldSummary: summary},
		}
	}
	return docs
}

// overlayBranchDocs builds the OVERLAY pool corpus: a small branch-changeset-sized
// corpus in which overlayRareTerm occurs in EVERY document (so the term is
// maximally COMMON there and its idf collapses), plus one document reusing the
// BASE pool's shared id with different body text.
func overlayBranchDocs() []searchengine.Document {
	docs := make([]searchengine.Document, 0, overlayBranchDocN+1)
	for i := range overlayBranchDocN {
		docs = append(docs, searchengine.Document{
			ID:     fmt.Sprintf("%s%d", overlayBranchPrefix, i),
			Vector: overlayVec(0xB00C, i),
			Fields: map[string]string{
				searchengine.FieldSummary: overlayRareTerm + " branch changeset body",
			},
		})
	}
	docs = append(docs, searchengine.Document{
		ID:     overlaySharedID,
		Vector: overlayVec(0xB00C, overlayBranchDocN),
		Fields: map[string]string{
			searchengine.FieldSummary: strings.Join(
				[]string{overlayRareTerm, overlaySharedTerm, "branch changeset body"}, " "),
		},
	})
	return docs
}

// countID counts how many times an id appears in a hit list.
func countID(hits []searchengine.Hit, id string) int {
	n := 0
	for _, h := range hits {
		if h.ID == id {
			n++
		}
	}
	return n
}

// hasPrefixID reports whether any hit's id carries the given prefix — the probe
// for "the overlay pool actually contributed to this list".
func hasPrefixID(hits []searchengine.Hit, prefix string) bool {
	for _, h := range hits {
		if strings.HasPrefix(h.ID, prefix) {
			return true
		}
	}
	return false
}

// TestSearchOverlayRanksByComparableScores is the primary regression: a branch
// overlay must EARN its slots on score rather than inherit half the result set
// from a rank-interleaved merge.
//
// Text-only (no query vector), so only the BM25 arm runs and the ranking is
// deterministic. The query names both fixture terms so the shared document id is
// a genuine hit in BOTH pools and the dedup path is exercised, not assumed.
func TestSearchOverlayRanksByComparableScores(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, gc := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))

	seedShippedFields(t, ctx, mgr, kgtypes.GraphCode, overlayBaseGraph, overlayBaseDocs())
	seedShippedFields(t, ctx, mgr, kgtypes.GraphCode, overlayBranchGraph, overlayBranchDocs())

	query := overlayRareTerm + " " + overlaySharedTerm

	// KNOWN-POSITIVE CONTROLS, both pools, BEFORE the assertion under test. Without
	// them an overlay pool that returned nothing would satisfy every ordering claim
	// below for entirely the wrong reason.
	baseOnly, err := mgr.Search(ctx, kgtypes.GraphCode, overlayBaseGraph, query, nil, 10)
	require.NoError(t, err)
	require.NotEmpty(t, baseOnly, "control: the base pool alone returns hits")
	require.Equal(t, overlayTargetID, baseOnly[0].ID, "control: the base pool's own #1 is the true match")

	branchOnly, err := mgr.Search(ctx, kgtypes.GraphCode, overlayBranchGraph, query, nil, 10)
	require.NoError(t, err)
	require.NotEmpty(t, branchOnly, "control: the overlay pool alone MATCHES the query and returns hits")

	fused, err := mgr.SearchOverlay(ctx, kgtypes.GraphCode,
		overlayBaseGraph, overlayBranchGraph, query, nil, 10)
	require.NoError(t, err)
	require.NotEmpty(t, fused, "the two-pool search returns hits")

	require.Equal(t, overlayTargetID, fused[0].ID,
		"the query's only true match is base-resident and must not be outranked by changeset documents")

	require.True(t, hasPrefixID(fused, overlayBranchPrefix),
		"the overlay pool is ORDERED, not dropped: at least one overlay-resident id survives the merge")

	require.Equal(t, 1, countID(fused, overlaySharedID),
		"a document id present in BOTH pools appears exactly once — dedup survives the merge")
}

// TestSearchOverlayAdmitsBaseAndOverlay pins the admission requirement for
// overlays, which is that their base graphs count as being touched as well. One
// SearchOverlay call runs the per-pool preamble for BOTH pools, so both admission
// recordings fire inside a single method.
func TestSearchOverlayAdmitsBaseAndOverlay(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		admitted []string
	)
	record := func(gt kgtypes.GraphType, name string) {
		mu.Lock()
		defer mu.Unlock()
		admitted = append(admitted, string(gt)+"/"+name)
	}
	recorded := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), admitted...)
	}

	mgr := NewManager(loginStateStub{loggedIn: false}, t.TempDir(), 0, WithGraphAdmitter(record))

	// Cold manager, no shipped segments: the result is empty rather than an error,
	// and the admissions happen before any load.
	_, err := mgr.SearchOverlay(context.Background(), kgtypes.GraphCode,
		"repoA", "repoA@br", "hello", nil, 10)
	require.NoError(t, err)

	got := recorded()
	require.Len(t, got, 2, "one two-pool search records exactly two admissions")
	require.ElementsMatch(t, []string{"code/repoA", "code/repoA@br"}, got,
		"both the base graph and its overlay count as touched")
}

// TestSearchOverlayFallsBackWhenOverlayPoolEmpty pins default-branch behavior: a
// repo whose overlay pool was never shipped must rank exactly as it did before
// the two-pool arm existed.
func TestSearchOverlayFallsBackWhenOverlayPoolEmpty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, gc := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))

	seedShippedFields(t, ctx, mgr, kgtypes.GraphCode, overlayBaseGraph, overlayBaseDocs())

	query := overlayRareTerm + " " + overlaySharedTerm

	want, err := mgr.Search(ctx, kgtypes.GraphCode, overlayBaseGraph, query, nil, 10)
	require.NoError(t, err)
	require.NotEmpty(t, want, "control: the base ranking this fallback must reproduce is non-empty")

	got, err := mgr.SearchOverlay(ctx, kgtypes.GraphCode,
		overlayBaseGraph, overlayBranchGraph, query, nil, 10)
	require.NoError(t, err)
	require.Equal(t, hitIDs(want), hitIDs(got),
		"an absent overlay pool degrades to EXACTLY the base ranking")
}

// TestSearchOverlayFusesHNSWArmAcrossPools covers the arm no text-only test can
// reach: the HNSW half of the per-modality merge is skipped entirely when the
// query carries no vector, so a BM25-only merge would satisfy every other test
// here unchanged.
//
// The overlay-resident document carries NO query term, so only the vector arm
// can place it in the fused list. The nil-vector negative control is what
// distinguishes a working HNSW merge from a BM25 coincidence.
func TestSearchOverlayFusesHNSWArmAcrossPools(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, gc := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))

	// BASE: the standard fixture — BM25-strong for the rare term.
	baseDocs := overlayBaseDocs()
	seedShipped(t, ctx, mgr, kgtypes.GraphCode, overlayBaseGraph, baseDocs)
	seedShippedFields(t, ctx, mgr, kgtypes.GraphCode, overlayBaseGraph, baseDocs)

	// OVERLAY: documents that carry NO query term at all. One of them owns the
	// vector we will query with, making it the exact-match nearest neighbor.
	const vectorOnlyID = "vec-only-target"
	branchDocs := make([]searchengine.Document, 0, overlayBranchDocN+1)
	for i := range overlayBranchDocN {
		branchDocs = append(branchDocs, searchengine.Document{
			ID:     fmt.Sprintf("%s%d", overlayBranchPrefix, i),
			Vector: overlayVec(0xB00C, i),
			Fields: map[string]string{searchengine.FieldSummary: "branch changeset body"},
		})
	}
	queryVec := overlayVec(0xFEED, 1)
	branchDocs = append(branchDocs, searchengine.Document{
		ID:     vectorOnlyID,
		Vector: queryVec,
		Fields: map[string]string{searchengine.FieldSummary: "branch changeset body"},
	})
	seedShipped(t, ctx, mgr, kgtypes.GraphCode, overlayBranchGraph, branchDocs)
	seedShippedFields(t, ctx, mgr, kgtypes.GraphCode, overlayBranchGraph, branchDocs)

	// Control: the overlay pool's vector arm really does rank that document first.
	branchOnly, err := mgr.Search(ctx, kgtypes.GraphCode, overlayBranchGraph, overlayRareTerm, queryVec, 10)
	require.NoError(t, err)
	require.NotEmpty(t, branchOnly, "control: the overlay pool's vector arm returns hits")

	withVec, err := mgr.SearchOverlay(ctx, kgtypes.GraphCode,
		overlayBaseGraph, overlayBranchGraph, overlayRareTerm, queryVec, 10)
	require.NoError(t, err)
	require.Contains(t, hitIDs(withVec), vectorOnlyID,
		"only the HNSW half of the merge can place a document that carries no query term")

	// NEGATIVE CONTROL: same fixture, no query vector. The HNSW arm is skipped, so
	// that id must be absent — otherwise the assertion above proves nothing.
	noVec, err := mgr.SearchOverlay(ctx, kgtypes.GraphCode,
		overlayBaseGraph, overlayBranchGraph, overlayRareTerm, nil, 10)
	require.NoError(t, err)
	require.NotContains(t, hitIDs(noVec), vectorOnlyID,
		"with no query vector the HNSW arm is skipped and the vector-only document cannot appear")
}

// TestSearchOverlayPoolErrors pins the ASYMMETRIC error contract in both
// directions. Pool selectivity comes from the warm/cold asymmetry: load()
// short-circuits on its once-guard, so a pool already searched never touches the
// source again, while the other pool's cold cache falls through to the tripped
// source.
func TestSearchOverlayPoolErrors(t *testing.T) {
	t.Parallel()

	query := overlayRareTerm + " " + overlaySharedTerm

	t.Run("base_hard_fails", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		_, gc := newSegmentHarness(t)
		fail := &failAfterWarmSource{inner: gc}
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(fail))

		// Warm the OVERLAY pool only; the base pool stays cold.
		seedShippedFields(t, ctx, mgr, kgtypes.GraphCode, overlayBranchGraph, overlayBranchDocs())
		warm, err := mgr.Search(ctx, kgtypes.GraphCode, overlayBranchGraph, query, nil, 10)
		require.NoError(t, err)
		require.NotEmpty(t, warm, "control: the overlay pool is warm and serving before the source is tripped")

		fail.trip()

		hits, err := mgr.SearchOverlay(ctx, kgtypes.GraphCode,
			overlayBaseGraph, overlayBranchGraph, query, nil, 10)
		require.Error(t, err, "a base-pool failure is returned, never served as an overlay-only result set")
		require.Nil(t, hits)
	})

	t.Run("overlay_degrades", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		_, gc := newSegmentHarness(t)
		fail := &failAfterWarmSource{inner: gc}
		mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(fail))

		// Warm the BASE pool only; the overlay pool stays cold.
		seedShippedFields(t, ctx, mgr, kgtypes.GraphCode, overlayBaseGraph, overlayBaseDocs())
		want, err := mgr.Search(ctx, kgtypes.GraphCode, overlayBaseGraph, query, nil, 10)
		require.NoError(t, err)
		require.NotEmpty(t, want, "control: the base ranking the degrade must reproduce is non-empty")

		fail.trip()

		got, err := mgr.SearchOverlay(ctx, kgtypes.GraphCode,
			overlayBaseGraph, overlayBranchGraph, query, nil, 10)
		require.NoError(t, err, "an overlay-pool failure degrades to the base pool rather than failing the search")
		require.Equal(t, hitIDs(want), hitIDs(got),
			"the degraded result is EXACTLY the base ranking")
	})
}

// TestSearchOverlayAbsentTokenYieldsNoOverlayDomination is the degenerate-ranking
// control: a query no document matches must not hand the overlay pool the result
// set anyway. The absence of the token from BOTH corpora is ASSERTED from the
// constructed documents first — a token that turns out to be a real corpus term
// makes every hit a legitimate match and the test proves nothing.
func TestSearchOverlayAbsentTokenYieldsNoOverlayDomination(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, gc := newSegmentHarness(t)
	mgr := NewManager(loginStateStub{loggedIn: true}, t.TempDir(), 0, withSegmentSource(gc))

	baseDocs := overlayBaseDocs()
	branchDocs := overlayBranchDocs()

	const absentToken = "qqzzvvwwabsentnowhere"
	for _, docs := range [][]searchengine.Document{baseDocs, branchDocs} {
		for _, d := range docs {
			for _, body := range d.Fields {
				require.NotContains(t, body, absentToken,
					"control: the probe token must appear in NO document of EITHER corpus")
			}
		}
	}

	seedShippedFields(t, ctx, mgr, kgtypes.GraphCode, overlayBaseGraph, baseDocs)
	seedShippedFields(t, ctx, mgr, kgtypes.GraphCode, overlayBranchGraph, branchDocs)

	// Known-positive control on the same wiring: a token that IS present returns
	// hits, so an empty result below means "no match", not "search is broken".
	present, err := mgr.SearchOverlay(ctx, kgtypes.GraphCode,
		overlayBaseGraph, overlayBranchGraph, overlayRareTerm, nil, 10)
	require.NoError(t, err)
	require.NotEmpty(t, present, "control: a token present in the corpora does return hits")

	hits, err := mgr.SearchOverlay(ctx, kgtypes.GraphCode,
		overlayBaseGraph, overlayBranchGraph, absentToken, nil, 10)
	require.NoError(t, err)
	require.False(t, hasPrefixID(hits, overlayBranchPrefix),
		"a token absent from both corpora returns no overlay-dominated result set")
}
