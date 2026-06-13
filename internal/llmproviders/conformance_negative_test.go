// SPDX-License-Identifier: Apache-2.0

package llmproviders

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// negativeFixture is one provider's recorded Markdown-instead-of-JSON failure
// body. prose is the raw model text the provider's fake serves (wrapped by the
// row's markdownEnvelope). Every prose value is a GENUINE negative: it carries
// NO balanced JSON value substring that decodes to a summariesPayload with a
// non-empty summary, so the summarizer's tolerant fallback (stripCodeFences +
// extractFirstJSONValue, then a retry decode) reaches the end and yields
// nothing — verified directly against parseSummariesContent by
// TestConformance_NegativeFixturesDefeatTolerantParse before any row runs.
type negativeFixture struct {
	prose string
	// fenced marks a fixture that wraps its prose in a Markdown code fence
	// (```json …) so the tolerant path's stripCodeFences + extractFirstJSONValue
	// are PROVABLY exercised and still yield nothing decodable — not merely that
	// the bare json.Unmarshal fast path failed.
	fenced bool
}

// negativeFixtures is the per-provider Markdown failure body. The shapes mirror
// the recorded real-world regression: the model answered with prose summaries
// ("# Code Chunk Summaries …\n\n1. …") instead of the schema-constrained JSON
// object, and the content-hash cache had been hiding it on established graphs.
//
// claude-cli is the most load-bearing row: its prose rides the free-form result
// string with NO structured_output key (markdownEnvelope = claudeCLITextEnvelope),
// reproducing the original commentary-instead-of-structured_output bug.
//
// FIXTURE-DESIGN CONSTRAINT (load-bearing): none of these prose bodies may
// contain a balanced {…}/[…] span that decodes into a summariesPayload with a
// non-empty summary, or the tolerant extractFirstJSONValue path would recover it
// and the negative would become a SILENT SUCCESS. The safe shape is plain prose
// with no JSON object/array at all; the gemini fixture deliberately includes a
// brace-bearing inline-code span to exercise the brace scanner on a span that is
// NOT a decodable items payload.
var negativeFixtures = map[llm.Provider]negativeFixture{
	llm.ProviderAnthropic: {
		prose: "# Code Chunk Summaries & Keywords\n\n" +
			"1. The first chunk defines the package entrypoint and wires up the main loop.\n" +
			"   Keywords: main, entrypoint, bootstrap, loop, startup\n\n" +
			"Let me know if you'd like these in a different format.",
	},
	llm.ProviderOpenAI: {
		prose: "Here are the summaries you requested:\n\n" +
			"Chunk 1 — Implements the HTTP handler that routes inbound requests to the " +
			"correct service method and writes the JSON response.\n\n" +
			"Keywords: http, handler, router, request, response, dispatch",
	},
	llm.ProviderGemini: {
		// Pure prose, no brace/bracket pair at all: a fixture containing even an
		// empty `{}` would decode through extractFirstJSONValue into a valid
		// (zero-item) summariesPayload with a NIL error, defeating the
		// parse-must-error guard. The safe negative carries no JSON delimiter.
		prose: "## Summary\n\n" +
			"The chunk declares an empty main function and is the program entrypoint. " +
			"It performs no work on its own.\n\n" +
			"Top keywords: entrypoint, main, empty-body, noop",
	},
	llm.ProviderClaudeCLI: {
		// The original regression shape: prose commentary in `result`, NO
		// structured_output key. claudeCLITextEnvelope carries this.
		prose: "Sure! Here's a one-sentence summary for the chunk:\n\n" +
			"The code chunk is a minimal Go program whose main function is empty, " +
			"serving as a placeholder entrypoint.\n\n" +
			"Suggested keywords: golang, main, placeholder, entrypoint, minimal",
	},
	llm.ProviderCodexCLI: {
		// Fenced-Markdown-wrapping-prose fixture: the ```json fence forces the
		// tolerant stripCodeFences + extractFirstJSONValue path, and the fenced
		// body is prose with no decodable items object — proving the tolerant
		// path was reached AND still yielded nothing.
		fenced: true,
		prose: "```json\n" +
			"# Summaries\n" +
			"1. A tiny Go program with an empty main; it is the entrypoint and does nothing.\n" +
			"Keywords: go, main, entrypoint, empty, noop\n" +
			"```",
	},
}

// TestConformance_NegativeFixturesDefeatTolerantParse is the load-bearing
// fixture-design guard. It calls the LIVE tolerant parser (parseSummariesContent
// — bare json.Unmarshal, then stripCodeFences + extractFirstJSONValue, then a
// retry decode) directly on each fixture's prose and asserts it ERRORS with an
// empty payload. A fixture whose prose hid a balanced JSON items span would slip
// through the tolerant path as a silent success and make the loud-failure row
// meaningless; this test fails first if that ever happens.
//
// It ALSO asserts at least one fixture is fenced, so the tolerant path's
// stripCodeFences + extractFirstJSONValue are PROVABLY exercised on a body that
// reaches them and still yields nothing — not merely that bare unmarshal failed.
func TestConformance_NegativeFixturesDefeatTolerantParse(t *testing.T) {
	sawFenced := false
	for _, p := range sortedProviders() {
		fx, ok := negativeFixtures[p]
		require.True(t, ok, "provider %q has a conformance row but no negative fixture", p)
		if fx.fenced {
			sawFenced = true
		}
		t.Run(string(p), func(t *testing.T) {
			parsed, err := parseSummariesContent(fx.prose)
			require.Error(t, err,
				"%s: fixture prose must NOT decode through the tolerant fallback (no balanced JSON items span). prose:\n%s", p, fx.prose)
			assert.Empty(t, parsed.Items,
				"%s: tolerant parse of the fixture must yield no items, got %+v", p, parsed.Items)
		})
	}
	assert.True(t, sawFenced,
		"at least one negative fixture must be fenced (```json …) to prove the tolerant stripCodeFences + extractFirstJSONValue path is exercised and still yields nothing")
}

// TestConformance_MarkdownResponseFailsLoud is the negative half of the gate:
// each provider's fake serves the recorded Markdown failure body in its native
// envelope, and SummarizeBatch must fail LOUD — a non-nil *llm.LLMError with a
// parse-failure reason and an empty summaries map. No silent success is
// representable.
//
// The fake serves the SAME body on every call, so the one billed repairParse
// retry re-sends the same prose and also fails: the terminal outcome is what's
// asserted, and >=1 endpoint hits are tolerated (the call count is NOT pinned).
func TestConformance_MarkdownResponseFailsLoud(t *testing.T) {
	for _, p := range sortedProviders() {
		row := conformanceCases[p]
		t.Run(string(p), func(t *testing.T) {
			fx := negativeFixtures[p]
			cc := row.newClient(t, row.markdownEnvelope(fx.prose))
			summ := NewLLMSummarizer(cc.client, row.provider, llm.Model(row.model))
			out, err := summ.SummarizeBatch(context.Background(), smokeChunks())

			require.Error(t, err, "%s: a Markdown reply must fail loud, not silently succeed", p)
			assert.Empty(t, out, "%s: a failed parse must yield an empty summaries map", p)

			var le *llm.LLMError
			require.ErrorAs(t, err, &le, "%s: failure must surface as *llm.LLMError", p)
			assert.False(t, le.Transient, "%s: a Markdown-instead-of-JSON reply is terminal, not transient", p)
			assert.Contains(t, parseFailureReasons, le.Reason,
				"%s: reason %q is not a recognized parse-failure reason (want one of %v)", p, le.Reason, parseFailureReasons)
		})
	}
}

// parseFailureReasons is the set of terminal *llm.LLMError reasons a genuine
// non-decodable Markdown reply produces. parse_summaries_json is the residual
// failure after the one repair retry (repairParse); empty_structured_output
// covers a payload that decoded but carried no non-empty summary. A loud
// Markdown failure lands in one of the two.
var parseFailureReasons = []string{"parse_summaries_json", "empty_structured_output"}
