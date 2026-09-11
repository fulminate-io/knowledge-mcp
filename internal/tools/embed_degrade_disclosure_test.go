// SPDX-License-Identifier: Apache-2.0

package tools

// embed_degrade_disclosure_test.go pins the ALWAYS-ON arm disclosure across every
// composer that embeds a query client-side.
//
// THE DEFECT CLASS THIS CLOSES: each of these arms called EmbedBinary and threw
// the error away, so a failed embed silently degraded a hybrid search to the BM25
// arm alone. Nothing about the rows says the semantic arm never ran, which is
// precisely why the label cannot be conditional on the result set — the degrade is
// LEAST visible when results ARE returned.
//
// EVERY TEST HERE IS TWO-DIRECTIONAL BY CONSTRUCTION. An assertion that only
// showed the label appearing could not tell "always on" from "always printed": a
// renderer hardcoding the string would satisfy it. Each subtest therefore drives
// the SAME composer twice — once with a failing embedder, once with a healthy one
// — and asserts the label differs.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// failingEmbedder is a BinaryEmbedder whose EmbedBinary always errors — the state
// every one of these arms previously discarded.
type failingEmbedder struct{ err error }

func (f failingEmbedder) Available() bool { return true }

func (f failingEmbedder) EmbedBinary(context.Context, string) ([]byte, error) {
	return nil, f.err
}

func (f failingEmbedder) EmbedBinaryBatch(_ context.Context, texts []string) ([][]byte, error) {
	return nil, f.err
}

// healthyEmbedder returns a usable vector, so the vector arm genuinely runs.
type healthyEmbedder struct{}

func (healthyEmbedder) Available() bool { return true }

func (healthyEmbedder) EmbedBinary(_ context.Context, text string) ([]byte, error) {
	return stubVec(text), nil
}

func (healthyEmbedder) EmbedBinaryBatch(_ context.Context, texts []string) ([][]byte, error) {
	out := make([][]byte, len(texts))
	for i, t := range texts {
		out[i] = stubVec(t)
	}
	return out, nil
}

// TestEmbedDegrade_DisclosedOnPracticeRenderers is the two-directional gate for
// both practice composers: the same call with a broken embedder must say
// BM25-only, and with a healthy one must say vector+text.
func TestEmbedDegrade_DisclosedOnPracticeRenderers(t *testing.T) {
	const embedFailure = "voyage: 429 rate limited"

	practiceBody := func(t *testing.T, emb any) string {
		t.Helper()
		gc := newFanOutHarness(t, []string{"default"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"default": {{ID: "p:go", Score: 0.90}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr, segCoverage: &gapCoverageFake{covered: 9}}
		switch e := emb.(type) {
		case failingEmbedder:
			deps.emb = e
		case healthyEmbedder:
			deps.emb = e
		}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Text: "pool",
		})
		return textBodyTools(res)
	}

	t.Run("the_one_combined_graph", func(t *testing.T) {
		// THE TWO SUBTESTS THAT USED TO SIT HERE ARE ONE. There was a
		// "single_language" leg driving a named pre-singleton graph and a
		// "no_selector" leg driving the unselected corpus-wide read; `language` is
		// refused on every practice arm now, so there is one read and one leg. The
		// disclosure contract is unchanged: a dead semantic arm is stated on the
		// render rather than inferred from flat scores.
		broken := practiceBody(t, failingEmbedder{err: errors.New(embedFailure)})
		assert.Contains(t, broken, "_search mode: BM25-only_",
			"a failed embed must be DISCLOSED on the render, not inferred from flat scores")
		assert.Contains(t, broken, "GoWorkerPool", "results are still served — this is disclosure, not refusal")

		healthy := practiceBody(t, healthyEmbedder{})
		assert.Contains(t, healthy, "_search mode: vector+text_",
			"a healthy embed must report the vector arm ran")
		// THE DISCRIMINATING LEG. Without it a renderer that hardcoded the
		// BM25-only string would satisfy the assertion above.
		assert.NotContains(t, healthy, "BM25-only",
			"the label must track the ACTUAL arm, not be printed unconditionally")
	})

	t.Run("zero_results_names_the_embed_failure", func(t *testing.T) {
		// An EMPTY result with a failed embed must NAME the failure rather than
		// report a confident no-match, and the graph must be covered so the
		// segment-gap branch does not pre-empt the embed disclosure.
		//
		// IT IS A NOTICE RATHER THAN AN ERROR, and that is the single-graph
		// contract this arm has always had — the arm that errored was the
		// scatter-gather, where one failed embed silently degraded eight graphs at
		// once and an empty merge was maximally misleading. The corpus is one graph
		// now, so the one-graph disclosure is the whole disclosure. What must not
		// weaken is the naming: the assertions below are on the failure TEXT
		// reaching the caller, which is what a silent degrade would lose.
		gc := newFanOutHarness(t, []string{"default"})
		deps := &interceptDeps{
			gc:          gc,
			segMgr:      newFanOutSegmentSearcher(nil),
			segCoverage: &gapCoverageFake{covered: 9},
			emb:         failingEmbedder{err: errors.New(embedFailure)},
		}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Text: "pool",
		})
		body := textBodyTools(res)
		assert.Contains(t, body, embedFailure, "the response names the embed failure")
		assert.Contains(t, body, "BM25-only", "and says which arm actually ran")
		assert.NotContains(t, strings.ToLower(body), "no matches")

		// THE DISCRIMINATING LEG: the same empty read with a HEALTHY embedder must
		// NOT name a failure, so the assertions above are the disclosure tracking
		// the embedder rather than a paragraph printed on every zero.
		healthyDeps := &interceptDeps{
			gc:          gc,
			segMgr:      newFanOutSegmentSearcher(nil),
			segCoverage: &gapCoverageFake{covered: 9},
			emb:         healthyEmbedder{},
		}
		healthy := textBodyTools(gatedRoutePractice(opCtx(), healthyDeps, gc, queryArgs{
			Graph: "practice", Text: "pool",
		}))
		assert.NotContains(t, healthy, embedFailure,
			"a healthy embedder must not produce a failure disclosure")
	})
}

// TestEmbedDegrade_DisclosedOnRegisteredGraphSearch is the same two-directional
// gate for the REGISTERED-CUSTOM composer.
//
// IT REPLACES THE PER-ACCOUNT RESOURCE COMPOSER'S ROW. That composer served the
// cicd family and went with it; the registered-custom search is what a contrib
// collector's inventory graph is read through now, and it carried the identical
// discard. The property is the composer's, not the family's, so the row moves
// rather than being deleted.
func TestEmbedDegrade_DisclosedOnRegisteredGraphSearch(t *testing.T) {
	const customFamily = "acme-ci"
	resourceBody := func(t *testing.T, emb any) (string, bool) {
		t.Helper()
		gc := newFanOutHarness(t, []string{"acme"},
			practiceNode("r1", "my-bucket", "a bucket"),
		)
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "r1", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, segMgr: mgr, gtCRUD: registeredGraphTypes(customFamily)}
		switch e := emb.(type) {
		case failingEmbedder:
			deps.emb = e
		case healthyEmbedder:
			deps.emb = e
		}
		res := composeRegisteredGraphSearch(opCtx(), deps, mgr,
			kgtypes.GraphType(customFamily), "acme", segmentSearchArgs{Query: "bucket"})
		return textBodyTools(res), res.IsError
	}

	broken, brokenErr := resourceBody(t, failingEmbedder{err: errors.New("voyage: 503")})
	require.False(t, brokenErr, "results were returned, so this is disclosure not refusal: %s", broken)
	assert.Contains(t, broken, "_search mode: BM25-only_")

	healthy, _ := resourceBody(t, healthyEmbedder{})
	assert.Contains(t, healthy, "_search mode: vector+text_")
	assert.NotContains(t, healthy, "BM25-only",
		"the label must track the ACTUAL arm, not be printed unconditionally")
}

// TestEmbedDegrade_ContextSeedMarksDegraded pins the thoughts-context site: a
// failed embed is a DEGRADED seed, because the seed is semantic and without a
// vector it ran BM25-only. The degraded flag is the signal that distinguishes
// "retrieval could not run" from "nothing relates".
func TestEmbedDegrade_ContextSeedMarksDegraded(t *testing.T) {
	seed := func(t *testing.T, emb any) bool {
		t.Helper()
		gc := newFanOutHarness(t, []string{"default"},
			practiceNode("k1", "SomeThought", "body"),
		)
		deps := &interceptDeps{
			gc:     gc,
			segMgr: &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "k1", Score: 0.9}}},
		}
		switch e := emb.(type) {
		case failingEmbedder:
			deps.emb = e
		case healthyEmbedder:
			deps.emb = e
		}
		_, degraded := composeContextSeed(opCtx(), deps, gc, "auth")
		return degraded
	}

	assert.True(t, seed(t, failingEmbedder{err: errors.New("voyage: 500")}),
		"a failed embed leaves the seed BM25-only, which is a degraded seed")
	// THE KNOWN POSITIVE: the same fixture with a working embedder must NOT be
	// degraded, or "degraded" would be constant and prove nothing.
	assert.False(t, seed(t, healthyEmbedder{}),
		"a healthy embed produces a non-degraded seed")
}
