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

	practiceBody := func(t *testing.T, language string, emb any) string {
		t.Helper()
		gc := newFanOutHarness(t, []string{"go", "python"},
			practiceNode("p:go", "GoWorkerPool", "bounded goroutines"),
			practiceNode("p:py", "PyThreadPool", "thread pool executor"),
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"go":     {{ID: "p:go", Score: 0.90}},
			"python": {{ID: "p:py", Score: 0.70}},
		})
		deps := &interceptDeps{gc: gc, segMgr: mgr, segCoverage: &gapCoverageFake{covered: 9}}
		switch e := emb.(type) {
		case failingEmbedder:
			deps.emb = e
		case healthyEmbedder:
			deps.emb = e
		}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Language: language, Text: "pool",
		})
		return textBodyTools(res)
	}

	t.Run("single_language", func(t *testing.T) {
		broken := practiceBody(t, "go", failingEmbedder{err: errors.New(embedFailure)})
		assert.Contains(t, broken, "_search mode: BM25-only_",
			"a failed embed must be DISCLOSED on the render, not inferred from flat scores")
		assert.Contains(t, broken, "GoWorkerPool", "results are still served — this is disclosure, not refusal")

		healthy := practiceBody(t, "go", healthyEmbedder{})
		assert.Contains(t, healthy, "_search mode: vector+text_",
			"a healthy embed must report the vector arm ran")
		// THE DISCRIMINATING LEG. Without it a renderer that hardcoded the
		// BM25-only string would satisfy the assertion above.
		assert.NotContains(t, healthy, "BM25-only",
			"the label must track the ACTUAL arm, not be printed unconditionally")
	})

	t.Run("fan_out", func(t *testing.T) {
		// The fan-out embeds ONCE and reuses that vector for every graph, so one
		// failed embed degrades the whole cross-graph ranking.
		broken := practiceBody(t, "all", failingEmbedder{err: errors.New(embedFailure)})
		assert.Contains(t, broken, "Searched 2 practice graphs", "the fan-out still ran")
		assert.Contains(t, broken, "_search mode: BM25-only_")

		healthy := practiceBody(t, "all", healthyEmbedder{})
		assert.Contains(t, healthy, "_search mode: vector+text_")
		assert.NotContains(t, healthy, "BM25-only")
	})

	t.Run("fan_out_zero_results_names_the_error", func(t *testing.T) {
		// An EMPTY fan-out with a failed embed must name the error rather than
		// report a confident no-match, and the graphs must be covered so the
		// segment-gap branch does not pre-empt the embed disclosure.
		gc := newFanOutHarness(t, []string{"go"})
		deps := &interceptDeps{
			gc:          gc,
			segMgr:      newFanOutSegmentSearcher(nil),
			segCoverage: &gapCoverageFake{covered: 9},
			emb:         failingEmbedder{err: errors.New(embedFailure)},
		}
		res := gatedRoutePractice(opCtx(), deps, gc, queryArgs{
			Graph: "practice", Language: "all", Text: "pool",
		})
		body := textBodyTools(res)
		assert.True(t, res.IsError, "an empty result set with a dead semantic arm is an error: %s", body)
		assert.Contains(t, body, embedFailure, "the response names the embed failure")
		assert.NotContains(t, strings.ToLower(body), "no matches")
	})
}

// TestEmbedDegrade_DisclosedOnResourceRenderer is the same two-directional gate
// for the cloud/cicd composer, which carried the identical discard.
func TestEmbedDegrade_DisclosedOnResourceRenderer(t *testing.T) {
	resourceBody := func(t *testing.T, emb any) (string, bool) {
		t.Helper()
		gc := newFanOutHarness(t, []string{"acme"},
			practiceNode("r1", "my-bucket", "a bucket"),
		)
		mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "r1", Score: 0.9}}}
		deps := &interceptDeps{gc: gc, segMgr: mgr}
		switch e := emb.(type) {
		case failingEmbedder:
			deps.emb = e
		case healthyEmbedder:
			deps.emb = e
		}
		res := composeResourceSearchClient(opCtx(), deps, mgr, cloudGraphKind, "acme", "bucket", "")
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
