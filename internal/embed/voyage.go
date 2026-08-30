// SPDX-License-Identifier: Apache-2.0

// Package embed provides the knowledge client's binary-embedding arms and
// the registry that dispatches between them.
//
// It lives under cmd/knowledge/internal/ deliberately: the OSS knowledge-server
// binary (cmd/knowledge-server) must carry ZERO LLM capability (by design —
// the server is a generic graph toolbox). Go's internal/
// visibility makes this package STRUCTURALLY unreachable from any binary
// outside the cmd/knowledge subtree — the server cannot import it even by
// accident, so the Voyage HTTP embedding code can never reach a server binary.
// That compiler-enforced boundary is stronger than the prior pkg/ link
// discipline (which relied on the server merely declining to import it).
//
// The sole consumer is the LLM-key-holding client, cmd/knowledge: it
// embeds content during the client-side index pipeline and embeds the query
// text at search time, then ships the vectors over the wire. No server binary
// embeds — they store and search the client-supplied vectors only.
//
// The BinaryEmbedder interface contract lives in embedder.go beside the arms
// that implement it; each arm is one file in this package and registers
// itself from its own init(). Mirrors the sibling
// cmd/knowledge/internal/rerank layout for the client-side reranker.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// DefaultBaseURL is the Voyage AI API endpoint this arm posts to when the
// resolved config supplies no base_url. This file is the ONE home of the
// endpoint literal for the embed axis.
const DefaultBaseURL = "https://api.voyageai.com"

// DefaultModel is the model this arm uses when the resolved config
// supplies an empty model — the ordinary no-config case. The ARM owns its
// default rather than the config layer: config is a leaf with no provider
// knowledge, and a provider model name baked into the generic config layer
// is exactly the pollution the separate embed vocabulary exists to avoid.
//
// THE WITHHELD DEFAULT FLIP, recorded here rather than left as an
// omission a later reader mistakes for an oversight.
//
// The intended next default is voyage-code-4. It is NOT set here, and the
// precondition is the embed cache key: the key is content-derived only, so
// changing the model re-embeds NOTHING — unchanged content hits the cache
// and the gap is suppressed. Widening the cache key is a client-to-server
// wire change awaiting sign-off; the widened embed cache key is the
// precondition, and it follows from the provider-registry architecture
// decision.
//
// The consequence of flipping without re-embedding is worse than a no-op,
// and it is silent. One config drives BOTH the index pipeline's embedder
// and the search-time query embedder. The corpus would stay on the old
// model's vectors while every query vector became the new model's, and the
// two would be compared by hamming distance in the vector-index traverse
// path. Both are 32 bytes, so every length guard stays quiet: the failure
// mode is degraded search with no error, no panic and no log line.
//
// An operator CAN set the model by hand in [embedder] today and hit
// exactly this hazard; construction warns when the resolved model is not
// this default.
const DefaultModel = "voyage-code-3"

// voyageEmbedder calls the Voyage AI API for binary code embeddings.
type voyageEmbedder struct {
	APIKey    string
	BaseURL   string
	Model     string
	Dimension int
	Dtype     string
	InputRole InputRole
	client    *http.Client
}

func init() { RegisterProvider(ProviderVoyage, newVoyageFromConfig) }

// newVoyageFromConfig is the registered factory. Empty cfg.BaseURL and
// empty cfg.Model fall back to this arm's own defaults.
//
// The base-URL chain mirrors the OpenAI summarizer service's exactly. The
// MODEL half deliberately DIVERGES: that service has no default model and
// ERRORS on an empty one, which is right for a summarizer (no single model
// is a sensible default and guessing one picks the operator's cost profile
// for them) and wrong for an embed arm, where the provider has one obvious
// default and an empty model is the ordinary no-config case. Do not
// "fix" this arm into erroring.
func newVoyageFromConfig(_ context.Context, cfg *Config) (BinaryEmbedder, error) {
	if cfg == nil {
		return nil, errNilConfig()
	}
	e := &voyageEmbedder{
		APIKey:    cfg.APIKey,
		BaseURL:   cfg.BaseURL,
		Model:     cfg.Model,
		Dimension: cfg.Dimension,
		Dtype:     cfg.Dtype,
		InputRole: cfg.InputRole,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
	if e.BaseURL == "" {
		e.BaseURL = DefaultBaseURL
	}
	if e.Model == "" {
		// Filled through defaultModelFor rather than from the constant directly,
		// so the model this arm sends and the model ResolveIdentity states are
		// produced by one function and cannot drift. See identity.go.
		e.Model = defaultModelFor(ProviderVoyage)
	}
	if e.InputRole == "" {
		e.InputRole = InputRoleDocument
	}
	return e, nil
}

// newVoyageEmbedder creates an embedder at this arm's defaults, with the
// accepted width and dtype supplied EXPLICITLY rather than left as zero
// values: Config.Validate refuses a zero Dimension, and the exported
// constructor below has no error return, so a refusal there would have
// nowhere to surface and would hand back either a panic or a nil embedder
// that reads as the documented BM25-only degrade.
func newVoyageEmbedder(apiKey string) *voyageEmbedder {
	cfg := &Config{
		Provider:  ProviderVoyage,
		APIKey:    apiKey,
		Dimension: 256,
		Dtype:     voyageDtypeUbinary,
		InputRole: InputRoleDocument,
	}
	e, err := newVoyageFromConfig(context.Background(), cfg)
	if err != nil {
		// newVoyageFromConfig only errors on a nil config, which cannot
		// happen here; the branch exists so the compiler is satisfied.
		return nil
	}
	arm, ok := e.(*voyageEmbedder)
	if !ok {
		// Equally unreachable: the factory just above returns exactly this
		// concrete type. Checked rather than asserted so a future factory
		// change surfaces as a nil here instead of a panic in a caller.
		return nil
	}
	return arm
}

// NewVoyageBinaryEmbedder is the exported constructor for a Voyage binary
// embedder at this arm's defaults, built without going through the
// registry or the config resolution. It has no in-tree callers: the
// client's llmproviders.BuildEmbedder now resolves through
// embed.NewEmbedder, and a repo-wide census (both call shapes, tests
// included) found no other. It stays exported as the direct-construction
// seam for a harness that holds a key and wants a real embedder with no
// config loaded. Returns BinaryEmbedder so callers keep type-qualifying
// against the interface (declared in embedder.go).
func NewVoyageBinaryEmbedder(apiKey string) BinaryEmbedder {
	return newVoyageEmbedder(apiKey)
}

// Compile-time assertion: *voyageEmbedder satisfies BinaryEmbedder.
var _ BinaryEmbedder = (*voyageEmbedder)(nil)

// Voyage enforces two independent caps per embeddings call: a max item count,
// and a max TOTAL token count across the batch, counted by its own tokenizer
// after per-item truncation. Exceeding the total fails the whole call with
// TOO_MANY_TOKENS_IN_BATCH, so packing by item count alone lets a batch of
// large texts fail every text in it. Packs are therefore also bounded by an
// estimated token budget. The estimate is deliberately conservative (few
// characters per token): estimator error can only shrink a pack, never
// overfill one.
const (
	// embedBatchSize is the max texts per Voyage API call.
	embedBatchSize = 128
	// batchTokenBudget bounds the estimated token total per call; the real
	// cap is 120k for the default model, and the gap is headroom for
	// estimator error.
	//
	// IT IS DELIBERATELY NOT RE-DERIVED for the model this arm's
	// DefaultModel comment names as the intended next default. That model's
	// cap WAS observed against the live API and recorded — see
	// testdata/ful1498_voyage_code4_verification.txt, which carries the raw
	// 400 body disclosing a 320000-token per-batch maximum. The budget
	// bounds packs for the model actually in use, and the default is not
	// changing here, so a figure derived from a model the client is not
	// talking to would be a wrong number wearing a citation. The observed
	// figure is larger than the current one, so carrying this budget across
	// a future flip under-packs rather than over-packs, and the bisection
	// path below recovers from an under-estimate regardless.
	batchTokenBudget = 100_000
	// charsPerToken is the conservative divisor for the token estimate.
	charsPerToken = 3
)

// estimateTokens conservatively estimates the Voyage token count of one text.
func estimateTokens(text string) int {
	return len(text)/charsPerToken + 1
}

type voyageEmbedRequest struct {
	Input      []string `json:"input"`
	Model      string   `json:"model"`
	InputType  string   `json:"input_type"`
	OutputDim  int      `json:"output_dimension"`
	OutputType string   `json:"output_dtype"`
}

// The two dtype vocabularies this arm speaks — the CONFIG spellings it decodes
// by and the WIRE spellings it asks in — and the translation between them live
// in voyage_dtype.go, split out of this file to keep it under the repo's
// per-file length gate.

// voyageEmbedResponse holds one batch's rows with each embedding LEFT
// UNDECODED, because the payload's element type depends on which
// representation the request asked for: ubinary rows are integers and float32
// rows are decimals, and no single Go slice type reads both without lying about
// one of them.
//
// DEFERRING THE DECODE IS WHAT MAKES THE MISMATCH DETECTABLE. The prior shape
// declared []int and so failed a float response inside encoding/json, producing
// a generic parse error that named neither what was asked for nor what came
// back. Holding the bytes lets decodeVoyageEmbedding refuse in terms the
// operator can act on.
type voyageEmbedResponse struct {
	Data []struct {
		Embedding json.RawMessage `json:"embedding"`
	} `json:"data"`
}

// decodeVoyageEmbedding turns one raw embedding row into the bytes every hop
// downstream carries, decoding it as the representation the REQUEST asked for.
//
// UBINARY is one byte per returned value, unchanged from what this arm has
// always done: Voyage returns the packed bits as an array of uint8 values and
// each becomes one byte.
//
// FLOAT32 is four bytes per returned value, LITTLE-ENDIAN, and the encoding is
// delegated to encodeFloat32LE rather than spelled out here: the byte order is
// a CONTRACT the v3 segment's float view reads, so a second copy of the loop
// would be a second chance to disagree with that reader, and the disagreement
// would be silent — finite, plausible, wrong distances rather than an error.
// That function is also what the two float-only arms encode through, so every
// float row this client emits is packed by one implementation.
//
// A REPRESENTATION MISMATCH IS REFUSED, NEVER COERCED. If ubinary was requested
// and the payload is not integer-valued, the request and the answer disagree
// about what these numbers ARE, and quantizing the decimals into bytes here
// would invent a representation nobody agreed on. The refusal names both sides.
//
// THE OPPOSITE DIRECTION IS NOT DETECTABLE AT THIS LAYER, and that is a fact
// about JSON rather than a check omitted here: every integer is a valid JSON
// decimal, so a ubinary payload returned under a float32 request parses as
// floats and cannot be distinguished from a float payload of whole numbers. The
// asymmetry is inherent to the encoding — a decimal is refusable as an integer,
// an integer is not refusable as a decimal.
func decodeVoyageEmbedding(raw json.RawMessage, dtype string) ([]byte, error) {
	if dtype == voyageDtypeFloat32 {
		var values []float64
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("voyage embed: response is not a %s array: %w", voyageDtypeFloat32, err)
		}
		return encodeFloat32LE(values), nil
	}

	var values []int
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf(
			"voyage embed: the request asked for dtype %q, whose values are integers, "+
				"but the response is not an integer array (%w); the request and the answer "+
				"disagree about what these numbers are, and reinterpreting them here would "+
				"invent a representation neither side asked for",
			dtype, err)
	}
	vec := make([]byte, len(values))
	for i, v := range values {
		vec[i] = byte(v)
	}
	return vec, nil
}

// EmbedBinary generates one binary embedding at the configured model,
// width and dtype.
func (e *voyageEmbedder) EmbedBinary(ctx context.Context, text string) ([]byte, error) {
	results, err := e.EmbedBinaryBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}
	return results[0], nil
}

// EmbedBinaryBatch generates binary embeddings for multiple texts, splitting
// them into API calls that respect both of Voyage's per-call caps: item count
// and total batch tokens. A text whose own estimate exceeds the budget is sent
// alone — Voyage truncates each item to its context limit before counting, so
// a single text cannot overflow the batch cap. Results preserve input order.
func (e *voyageEmbedder) EmbedBinaryBatch(ctx context.Context, texts []string) ([][]byte, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	var allResults [][]byte
	batchNo := 0
	start := 0
	for start < len(texts) {
		end := start + 1
		budget := estimateTokens(texts[start])
		for end < len(texts) && end-start < embedBatchSize {
			t := estimateTokens(texts[end])
			if budget+t > batchTokenBudget {
				break
			}
			budget += t
			end++
		}

		results, err := e.embedPackBisecting(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("batch %d: %w", batchNo, err)
		}
		allResults = append(allResults, results...)
		start = end
		batchNo++
	}
	return allResults, nil
}

// embedPackBisecting posts one pack and, when Voyage rejects it for total
// batch tokens anyway (the estimator undercounted), splits the pack in half
// and retries each side rather than failing every text in it. The recursion
// terminates because a single text cannot overflow the batch cap (per-item
// truncation happens before the count); any other error propagates unchanged.
func (e *voyageEmbedder) embedPackBisecting(ctx context.Context, texts []string) ([][]byte, error) {
	results, err := e.callVoyageBatch(ctx, texts)
	if err == nil || len(texts) < 2 || !isBatchTokenOverflow(err) {
		return results, err
	}

	mid := len(texts) / 2
	left, err := e.embedPackBisecting(ctx, texts[:mid])
	if err != nil {
		return nil, err
	}
	right, err := e.embedPackBisecting(ctx, texts[mid:])
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

// isBatchTokenOverflow recognizes Voyage's whole-batch token-cap rejection,
// the one 400 whose correct handling is splitting the batch, not failing it.
func isBatchTokenOverflow(err error) bool {
	var llmErr *llm.LLMError
	return errors.As(err, &llmErr) && llmErr.Cause != nil &&
		strings.Contains(llmErr.Cause.Error(), "TOO_MANY_TOKENS_IN_BATCH")
}

// callVoyageBatch posts one batch of texts to the Voyage embeddings API.
// Errors return as *llm.LLMError so the pipeline embed worker can distinguish
// transient (HTTP 429 / 5xx — retry next tick) from terminal (4xx-other /
// JSON parse — write embed_failure_reason marker). Batch wrapper above already
// passes the error through `fmt.Errorf("batch %d: %w", ...)` so errors.As
// traverses the wrap.
func (e *voyageEmbedder) callVoyageBatch(ctx context.Context, texts []string) ([][]byte, error) {
	// The wire spelling, never the config one. A dtype with no observed
	// translation is TERMINAL: the same value fails identically on every retry,
	// so classifying it retryable would loop a worker on a request that can
	// never be built.
	wireDtype, err := voyageWireDtype(e.Dtype)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "config", Cause: err}
	}

	body, err := json.Marshal(voyageEmbedRequest{
		Input:      texts,
		Model:      e.Model,
		InputType:  e.voyageInputType(),
		OutputDim:  e.Dimension,
		OutputType: wireDtype,
	})
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "marshal_request", Cause: err}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "create_request", Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, &llm.LLMError{Transient: true, Reason: "network", Cause: fmt.Errorf("voyage embed request: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &llm.LLMError{
			Transient:  llm.HTTPStatusToTransient(resp.StatusCode),
			Reason:     fmt.Sprintf("http_%d", resp.StatusCode),
			RetryAfter: llm.ParseRetryAfter(resp.Header),
			Cause:      fmt.Errorf("voyage embed: status %d: %s", resp.StatusCode, string(respBody)),
		}
	}

	var result voyageEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "decode_response", Cause: err}
	}

	vectors := make([][]byte, len(result.Data))
	for i, d := range result.Data {
		// Decoded as the representation THIS REQUEST asked for. What went out as
		// output_dtype is voyageWireDtype(e.Dtype), which is a different STRING
		// for the unquantized case but names the same representation, and the
		// translation is total over the values that reach here — an untranslated
		// dtype fails above, before any request is built — so the request and
		// the decode cannot drift apart.
		//
		// A row that does not decode is TERMINAL, not transient. The same bytes
		// will fail identically on every retry, so classifying it retryable would
		// loop a worker on a payload that can never succeed.
		vec, derr := decodeVoyageEmbedding(d.Embedding, e.Dtype)
		if derr != nil {
			return nil, &llm.LLMError{Transient: false, Reason: "decode_response", Cause: derr}
		}
		vectors[i] = vec
	}
	return vectors, nil
}

// voyageInputType maps the semantic InputRole onto VOYAGE'S OWN
// vocabulary, whose input_type is null|query|document. Each arm owns its
// spelling and no arm reads another's — Cohere and Gemini spell the same
// two roles differently.
func (e *voyageEmbedder) voyageInputType() string {
	if e.InputRole == InputRoleQuery {
		return "query"
	}
	return "document"
}

// Available checks if the API key is set.
func (e *voyageEmbedder) Available() bool {
	return e.APIKey != ""
}
