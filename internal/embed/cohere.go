// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// The COHERE TYPED ARM. It exists as its own arm — rather than riding the
// generic OpenAI-compatible one — because the REQUEST and the RESPONSE
// both diverge, on three points that each cost exactly one line of code
// here and would cost correctness anywhere else:
//
//  1. embedding_types is a LIST, not a scalar dtype field. The arm sends
//     exactly one element and reads that same key back out.
//  2. input_type is REQUIRED and uses Cohere's OWN vocabulary —
//     search_document and search_query, NOT Voyage's document/query.
//     Getting this wrong is a silent quality loss, not an error, which is
//     why the mapping is explicit and tested.
//  3. The response is keyed BY THE REQUESTED TYPE (embeddings.ubinary),
//     not the converged flat data[].embedding envelope.
//
// THIS ARM IS QUANTIZED-ONLY, and that is a claim about the ARM rather than
// about Cohere: the response decode below is integer-shaped, so the factory
// REFUSES every dtype but the quantized one, naming the dtype it refused and
// the one it can serve. cohereDtypeRefusal carries the reason and states what
// completing the arm would take.

// cohereDefaultBaseURL is the Cohere API endpoint this arm posts to when
// the resolved config supplies no base_url.
const cohereDefaultBaseURL = "https://api.cohere.com"

// cohereDefaultModel is the model this arm uses when the resolved config
// supplies an empty model.
const cohereDefaultModel = "embed-v4.0"

// cohereBatchSize is the max texts per Cohere embed call. 96 is Cohere's
// own documented per-call limit (docs.cohere.com/reference/embed), cited
// here in the existing comment style rather than with the
// none-documented marker the two undocumented arms carry — a marker
// meaning "no source exists" would be misleading on a constant that has
// one. It deliberately does not reuse the Voyage arm's cap: 128 is a
// Voyage fact, and sharing it would let one provider's limit govern
// another's.
const cohereBatchSize = 96

// cohereDtypeUbinary is the ONE representation this arm can serve, spelled as
// the RESOLVED CONFIG spells it — the same string config.AcceptedEmbedDtypes
// carries. It is a CAPABILITY STATEMENT first.
//
// IT IS ALSO, TODAY, THE VALUE THAT GOES ON THE WIRE, because this arm sends
// the resolved dtype straight through as embedding_types and reads it back as
// the response key, and the config vocabulary and Cohere's embedding_types
// vocabulary happen to agree at this one string. It is NOT split into a
// separate wire constant the way the openai-compatible arm's is: that arm's
// two vocabularies genuinely disagree, and inventing a second spelling here
// with nothing observed to back it is what the batch-overflow note above
// forbids for this arm.
const cohereDtypeUbinary = "ubinary"

// NO TOTAL-BATCH TOKEN BUDGET. Unlike Voyage, no whole-batch token cap is
// documented for Cohere in the cited reference, so packs are bounded by
// ITEM COUNT ONLY and a server-side rejection surfaces as an http_<code>
// LLMError rather than being pre-empted here.
//
// NO BISECTION EITHER. The Voyage arm splits a pack when the response
// carries that provider's specific whole-batch-overflow code. That is a
// Voyage error string a different provider cannot be assumed to produce,
// and no Cohere equivalent was observed. Handling code for a batch
// overflow here must be written against an OBSERVED error body, never
// invented from imagination.

type cohereEmbedder struct {
	APIKey    string
	BaseURL   string
	Model     string
	Dimension int
	// Dtype is the representation this instance agreed to serve. It is
	// carried on the struct, rather than only checked at the factory, so the
	// methods can re-check it: every test in this package constructs an arm
	// directly, bypassing the factory entirely.
	Dtype     string
	InputRole InputRole
	client    *http.Client
}

// Compile-time assertion: *cohereEmbedder satisfies BinaryEmbedder.
var _ BinaryEmbedder = (*cohereEmbedder)(nil)

func init() { RegisterProvider(ProviderCohere, newCohereFromConfig) }

// newCohereFromConfig is the registered factory. It REFUSES any dtype other
// than cohereDtypeUbinary. Empty cfg.BaseURL and empty cfg.Model fall back to
// this arm's own defaults.
func newCohereFromConfig(_ context.Context, cfg *Config) (BinaryEmbedder, error) {
	if cfg == nil {
		return nil, errNilConfig()
	}
	if err := cohereDtypeRefusal(cfg.Dtype); err != nil {
		return nil, err
	}
	e := &cohereEmbedder{
		APIKey:    cfg.APIKey,
		BaseURL:   cfg.BaseURL,
		Model:     cfg.Model,
		Dimension: cfg.Dimension,
		Dtype:     cfg.Dtype,
		InputRole: cfg.InputRole,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
	if e.BaseURL == "" {
		e.BaseURL = cohereDefaultBaseURL
	}
	if e.Model == "" {
		// Through defaultModelFor so the model sent and the model
		// ResolveIdentity states are one value. See identity.go.
		e.Model = defaultModelFor(ProviderCohere)
	}
	if e.InputRole == "" {
		e.InputRole = InputRoleDocument
	}
	return e, nil
}

// cohereDtypeRefusal is the arm's one capability statement, shared by the
// factory and the methods so a directly-constructed instance cannot hand back
// a representation it never agreed to serve.
//
// THE REFUSAL NAMES BOTH SIDES — the value it refused AND the value it can
// serve — following the unknownEmbedProviderError shape in package config: an
// operator who lands here must not have to guess the spelling that works. A
// refusal that named only the bad value is how a legitimate configuration
// becomes indistinguishable from a typo.
//
// THE MECHANICAL REASON IS IN THIS PACKAGE, NOT AT THE PROVIDER.
// cohereEmbedResponse below decodes into map[string][][]int and callCohereBatch
// narrows each element with byte(v). An unquantized embedding is an array of
// DECIMALS, which cannot unmarshal into an int at all — so this arm could not
// decode a correct unquantized answer under ANY wire spelling, and asking for
// one was never a working configuration. The admitted config value "float32"
// previously constructed here, logged a ready embedder and became a graph's
// recorded embed identity before failing every embed call; refusing it moves
// that failure to the seam where it is legible.
//
// WHAT WOULD LIFT THIS REFUSAL, so the next reader knows the path rather than
// the wall: the decode half is ordinary work in this package — hold the
// embeddings map as json.RawMessage and branch per requested type, the shape
// voyage.go already uses for exactly this reason. The wire half is not: it
// needs Cohere's real embedding_types spelling for the unquantized type,
// established by ONE live verified call and recorded in the testdata/
// verification-note form this package already uses, never invented from
// imagination — the same rule the batch-overflow note above states for this arm.
func cohereDtypeRefusal(dtype string) error {
	if dtype == cohereDtypeUbinary {
		return nil
	}
	return fmt.Errorf("%w: the cohere arm cannot produce dtype %q (accepted: %s) — this arm decodes every embedding response into map[string][][]int, so an unquantized answer cannot be decoded under any wire spelling", ErrInvalidConfig, dtype, cohereDtypeUbinary)
}

// cohereEmbedRequest is Cohere's embed envelope. EmbeddingTypes is a LIST
// even though this arm always sends exactly one element — that is the
// wire shape, and flattening it to a scalar would be rejected.
type cohereEmbedRequest struct {
	Texts          []string `json:"texts"`
	Model          string   `json:"model"`
	InputType      string   `json:"input_type"`
	EmbeddingTypes []string `json:"embedding_types"`
	OutputDim      int      `json:"output_dimension,omitempty"`
}

// cohereEmbedResponse is keyed by the REQUESTED embedding type rather than
// the converged flat envelope, so the decode reads a map rather than a
// fixed field name.
type cohereEmbedResponse struct {
	Embeddings map[string][][]int `json:"embeddings"`
}

// cohereInputType maps the semantic InputRole onto COHERE'S OWN
// vocabulary. These are not Voyage's spellings and no arm reads another's.
func (e *cohereEmbedder) cohereInputType() string {
	if e.InputRole == InputRoleQuery {
		return "search_query"
	}
	return "search_document"
}

// Available reports whether this arm has something to authenticate with.
func (e *cohereEmbedder) Available() bool {
	return e.APIKey != "" || e.BaseURL != cohereDefaultBaseURL
}

// EmbedBinary generates one embedding at the configured model, width and
// dtype.
func (e *cohereEmbedder) EmbedBinary(ctx context.Context, text string) ([]byte, error) {
	results, err := e.EmbedBinaryBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}
	return results[0], nil
}

// EmbedBinaryBatch splits texts into packs bounded by this arm's own item
// cap and posts one call per pack. Results preserve input order.
//
// It re-checks the dtype for the reason cohereDtypeRefusal states: an instance
// built directly rather than through the factory has passed no gate.
func (e *cohereEmbedder) EmbedBinaryBatch(ctx context.Context, texts []string) ([][]byte, error) {
	if err := cohereDtypeRefusal(e.Dtype); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return nil, nil
	}
	var all [][]byte
	for i, pack := range packByCount(texts, cohereBatchSize) {
		results, err := e.callCohereBatch(ctx, pack)
		if err != nil {
			return nil, fmt.Errorf("batch %d: %w", i, err)
		}
		all = append(all, results...)
	}
	return all, nil
}

// callCohereBatch posts one pack. Errors return as *llm.LLMError with the
// SAME Reason vocabulary every arm uses, because the pipeline's
// retry-vs-terminal split reads it: an arm returning a bare error would
// reclassify every transient failure as terminal and write failure markers
// on rows that should simply have retried on the next tick.
func (e *cohereEmbedder) callCohereBatch(ctx context.Context, texts []string) ([][]byte, error) {
	body, err := json.Marshal(cohereEmbedRequest{
		Texts:          texts,
		Model:          e.Model,
		InputType:      e.cohereInputType(),
		EmbeddingTypes: []string{e.Dtype},
		OutputDim:      e.Dimension,
	})
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "marshal_request", Cause: err}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/v2/embed", bytes.NewReader(body))
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "create_request", Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, &llm.LLMError{Transient: true, Reason: "network", Cause: fmt.Errorf("cohere embed request: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &llm.LLMError{
			Transient:  llm.HTTPStatusToTransient(resp.StatusCode),
			Reason:     fmt.Sprintf("http_%d", resp.StatusCode),
			RetryAfter: llm.ParseRetryAfter(resp.Header),
			Cause:      fmt.Errorf("cohere embed: status %d: %s", resp.StatusCode, string(respBody)),
		}
	}

	var result cohereEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "decode_response", Cause: err}
	}
	rows, ok := result.Embeddings[e.Dtype]
	if !ok {
		return nil, &llm.LLMError{
			Transient: false, Reason: "decode_response",
			Cause: fmt.Errorf("cohere embed: response carries no %q key (got %d keys)", e.Dtype, len(result.Embeddings)),
		}
	}
	vectors := make([][]byte, len(rows))
	for i, row := range rows {
		vec := make([]byte, len(row))
		for j, v := range row {
			vec[j] = byte(v)
		}
		vectors[i] = vec
	}
	return vectors, nil
}
