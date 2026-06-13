// SPDX-License-Identifier: Apache-2.0

// The conformance suite is IN-PACKAGE (package llmproviders) so it can call the
// unexported parseSummariesContent directly for the negative-fixture pre-verify.
// It does NOT blank-import the provider sub-packages here — claude-cli / codex-cli
// transitively import llmproviders (via graphclient → hivemonitor → the
// supervisor handler), so an in-package import would be a cycle. The provider
// factory registrations instead run from conformance_register_test.go, an
// EXTERNAL (package llmproviders_test) file in this same directory whose blank
// imports populate the process-global llm registry at test-binary init — visible
// to this in-package suite through llm.NewClient / llm.ListProviders without any
// in-package import of the providers.
package llmproviders

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// conformanceCase is one provider's row in the fresh-summary conformance gate.
// Every registered provider has exactly one row (enforced set-equal to
// llm.ListProviders() by TestConformance_CoversEveryRegisteredProvider). A row
// is fully self-describing: it knows how to build a real client against a fake
// endpoint, what model to drive it with, how to wrap a payload in the provider's
// native success / Markdown-failure envelope, and how to assert the provider's
// structured-output wire mechanism.
type conformanceCase struct {
	// provider is the registry key; the table is map-keyed by it too.
	provider llm.Provider

	// model is the model passed to the summarizer. Anthropic requires a native
	// (claude-4.5+) model so the wire carries output_config rather than the
	// forced-tool fallback; the others are free-form identifiers.
	model string

	// newClient builds a real llm.Client (through llm.NewClient, the PUBLIC
	// seam) wired to a fake endpoint that records the outbound request and
	// serves respBody. CLI rows t.Skip on windows inside this constructor.
	newClient func(t *testing.T, respBody string) conformanceClient

	// successEnvelope wraps an items-JSON payload in the provider's REAL native
	// success reply envelope.
	successEnvelope func(itemsJSON string) string

	// markdownEnvelope wraps prose in the provider's native reply envelope for
	// the negative (issue-recorded Markdown-instead-of-JSON) fixture.
	markdownEnvelope func(prose string) string

	// assertWire asserts the provider's structured-output mechanism is present
	// on the captured outbound request, naming the provider + mechanism on miss.
	assertWire func(t *testing.T, cap *capturedRequest)
}

// conformanceCases is the per-provider table. EXACTLY one row per registered
// provider — the registration coupling the ticket demands: a future provider
// registered without a row turns TestConformance_CoversEveryRegisteredProvider
// red at the table gate, before any behavior is even exercised.
var conformanceCases = map[llm.Provider]conformanceCase{
	llm.ProviderAnthropic: {
		provider: llm.ProviderAnthropic,
		// Native structured-output model so the wire carries output_config.
		model: "claude-haiku-4-5",
		newClient: func(t *testing.T, respBody string) conformanceClient {
			return newAPIClient(t, llm.ProviderAnthropic, "claude-haiku-4-5", respBody)
		},
		successEnvelope:  anthropicEnvelope,
		markdownEnvelope: anthropicEnvelope,
		assertWire:       assertAnthropicWire,
	},
	llm.ProviderOpenAI: {
		provider: llm.ProviderOpenAI,
		model:    "gpt-5-mini",
		newClient: func(t *testing.T, respBody string) conformanceClient {
			return newAPIClient(t, llm.ProviderOpenAI, "gpt-5-mini", respBody)
		},
		successEnvelope:  openaiEnvelope,
		markdownEnvelope: openaiEnvelope,
		assertWire:       assertOpenAIWire,
	},
	llm.ProviderGemini: {
		provider: llm.ProviderGemini,
		model:    "gemini-2.5-pro",
		newClient: func(t *testing.T, respBody string) conformanceClient {
			return newAPIClient(t, llm.ProviderGemini, "gemini-2.5-pro", respBody)
		},
		successEnvelope:  geminiEnvelope,
		markdownEnvelope: geminiEnvelope,
		assertWire:       assertGeminiWire,
	},
	llm.ProviderClaudeCLI: {
		provider: llm.ProviderClaudeCLI,
		model:    "claude-sonnet-4-5",
		newClient: func(t *testing.T, respBody string) conformanceClient {
			return newCLIClient(t, llm.ProviderClaudeCLI, "claude-sonnet-4-5", respBody)
		},
		successEnvelope: claudeCLIEnvelope,
		// The Markdown failure rides the free-form result string with NO
		// structured_output key — the recorded original failure shape.
		markdownEnvelope: claudeCLITextEnvelope,
		assertWire:       assertClaudeCLIWire,
	},
	llm.ProviderCodexCLI: {
		provider: llm.ProviderCodexCLI,
		model:    "gpt-5-codex",
		newClient: func(t *testing.T, respBody string) conformanceClient {
			return newCLIClient(t, llm.ProviderCodexCLI, "gpt-5-codex", respBody)
		},
		successEnvelope:  codexCLIEnvelope,
		markdownEnvelope: codexCLIEnvelope,
		assertWire:       assertCodexCLIWire,
	},
}

// TestConformance_CoversEveryRegisteredProvider is the registration coupling:
// the conformance table's key set must be EXACTLY llm.ListProviders(), checked
// in BOTH directions. A registered provider with no row (a new provider shipped
// without conformance coverage) is red; a stale table row for an unregistered
// provider is also red. This is a set-equality assertion, NOT a length check —
// len==5 on both sides could still hide a swapped pair.
func TestConformance_CoversEveryRegisteredProvider(t *testing.T) {
	registered := llm.ListProviders()

	tableSet := make(map[llm.Provider]struct{}, len(conformanceCases))
	for p, row := range conformanceCases {
		tableSet[p] = struct{}{}
		// Guard against a copy-paste row whose map key and provider field
		// disagree — both must name the same provider.
		assert.Equal(t, p, row.provider, "row keyed %q carries provider field %q", p, row.provider)
	}
	registeredSet := make(map[llm.Provider]struct{}, len(registered))
	for _, p := range registered {
		registeredSet[p] = struct{}{}
	}

	for _, p := range registered {
		if _, ok := tableSet[p]; !ok {
			t.Errorf("provider %q is registered but has NO conformance row — a new provider must ship with a conformance case", p)
		}
	}
	for p := range tableSet {
		if _, ok := registeredSet[p]; !ok {
			t.Errorf("conformance row %q names a provider that is NOT registered — stale table row", p)
		}
	}
}

// sortedProviders returns the conformance table's provider keys in a stable
// order so the per-provider subtests run deterministically.
func sortedProviders() []llm.Provider {
	out := make([]llm.Provider, 0, len(conformanceCases))
	for p := range conformanceCases {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// itemsJSON is the canonical valid summariesPayload body every success fixture
// wraps: one item with a non-empty summary + keywords, matching the single
// chunk the conformance batches request.
const itemsJSON = `{"items":[{"summary":"a concise chunk summary","keywords":["alpha","beta","gamma"]}]}`

// smokeChunks is the single-chunk batch the conformance round trips drive. One
// chunk keeps the schema's minItems/maxItems at 1 and the fixtures small.
func smokeChunks() []BatchChunk {
	return []BatchChunk{{ID: "chunk-1", Content: "package main\n\nfunc main() {}"}}
}

// TestConformance_SmokeRoundTrip is the Phase 1 seam check: every row builds a
// real client via llm.NewClient (BaseURL or CLIBin) and completes a
// SummarizeBatch against its fake endpoint serving the provider's native
// success envelope, yielding the requested chunk's summary.
func TestConformance_SmokeRoundTrip(t *testing.T) {
	for _, p := range sortedProviders() {
		row := conformanceCases[p]
		t.Run(string(p), func(t *testing.T) {
			cc := row.newClient(t, row.successEnvelope(itemsJSON))
			summ := NewLLMSummarizer(cc.client, row.provider, llm.Model(row.model))
			out, err := summ.SummarizeBatch(context.Background(), smokeChunks())
			require.NoError(t, err, "%s: smoke SummarizeBatch", p)
			require.Len(t, out, 1, "%s: one summary per requested chunk", p)
			assert.Equal(t, "a concise chunk summary", out["chunk-1"].Summary, "%s", p)
		})
	}
}

// TestConformance_WireCarriesStructuredOutput drives the REAL summarizer through
// each row's client and asserts the provider's structured-output mechanism rode
// the outbound request: anthropic output_config, openai response_format, gemini
// responseSchema, claude-cli --json-schema, codex-cli --output-schema. The
// production schema threading (summarizer_llm.go) is what is under test — each
// assertWire failure names the provider and the missing mechanism.
func TestConformance_WireCarriesStructuredOutput(t *testing.T) {
	for _, p := range sortedProviders() {
		row := conformanceCases[p]
		t.Run(string(p), func(t *testing.T) {
			cc := row.newClient(t, row.successEnvelope(itemsJSON))
			summ := NewLLMSummarizer(cc.client, row.provider, llm.Model(row.model))
			_, err := summ.SummarizeBatch(context.Background(), smokeChunks())
			require.NoError(t, err, "%s: SummarizeBatch", p)
			cc.capture.finish(t)
			row.assertWire(t, cc.capture)
		})
	}
}

// TestConformance_SuccessResponseParses serves each provider's native SUCCESS
// envelope carrying a valid items payload and asserts SummarizeBatch returns the
// parsed summary for the requested chunk — the response side of the fresh-summary
// chain, end-to-end per provider, through each provider's parseResponse plus the
// summarizer's parseSummariesContent.
func TestConformance_SuccessResponseParses(t *testing.T) {
	for _, p := range sortedProviders() {
		row := conformanceCases[p]
		t.Run(string(p), func(t *testing.T) {
			cc := row.newClient(t, row.successEnvelope(itemsJSON))
			summ := NewLLMSummarizer(cc.client, row.provider, llm.Model(row.model))
			out, err := summ.SummarizeBatch(context.Background(), smokeChunks())
			require.NoError(t, err, "%s: SummarizeBatch", p)
			require.NotEmpty(t, out, "%s: parsed summaries must be non-empty", p)
			got := out["chunk-1"]
			assert.Equal(t, "a concise chunk summary", got.Summary, "%s: summary", p)
			assert.Equal(t, "alpha beta gamma", got.Keywords, "%s: keywords", p)
		})
	}
}

// The negative half of the gate — TestConformance_MarkdownResponseFailsLoud —
// lands in the negative-fixture phase (conformance_negative_test.go).
