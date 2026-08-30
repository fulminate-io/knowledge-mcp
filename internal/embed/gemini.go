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

// The GEMINI TYPED ARM. It diverges from every sibling on three points:
// the endpoint is per-model and per-operation (:embedContent for one input,
// :batchEmbedContents for a batch) rather than one fixed path; the
// dimension knob is spelled outputDimensionality on the wire; and
// authentication is an x-goog-api-key header rather than an Authorization
// Bearer — do not copy the Bearer line from a sibling arm. The header and
// base-URL conventions here are the ones this repo already uses for
// Gemini's generation API.
//
// GEMINI IS FLOAT ONLY. Google publishes no quantized embedding output for
// either current embedding model, so the factory REFUSES every dtype but the
// unquantized one, naming the dtype it refused and the one it can serve — no
// client-side binarization, no fallback to another provider.
//
// THE DTYPE IT ACCEPTS IS SPELLED IN THE CONFIG'S VOCABULARY, NOT A PROVIDER'S.
// config.AcceptedEmbedDtypes spells the unquantized representation "float32",
// and that is the only spelling an operator can put in [embedder]; this arm
// states its capability in that same vocabulary so the two sets intersect. They
// did not: the gate read the literal "float", which no admitted config could
// carry, so every admitted dtype was refused by this arm while the arm's only
// accepted dtype was refused by Config.Validate before the factory ran. The arm
// was unconstructible under every configuration an operator could write, and
// nothing was red until a test intersected the two sets — see
// TestEveryRegisteredProvider_ConstructsAtSomeAdmittedDtype.
//
// THERE IS NO WIRE SPELLING TO CONFUSE THAT ONE WITH HERE: geminiEmbedRequest
// below carries no dtype or encoding field at all, so nothing this arm puts on
// the wire is derived from the capability constant. The sibling
// openai-compatible arm is where the distinction has teeth.

// geminiDefaultBaseURL is the endpoint this arm posts to when the resolved
// config supplies no base_url.
const geminiDefaultBaseURL = "https://generativelanguage.googleapis.com"

// geminiDefaultModel is the model this arm uses when the resolved config
// supplies an empty model.
const geminiDefaultModel = "gemini-embedding-001"

// geminiBatchSize is the max texts per :batchEmbedContents call.
//
// cap provenance: NONE IS DOCUMENTED. No per-request item cap is published
// for :batchEmbedContents in the research this arm was built from, so this
// value is a conservative CLIENT-SIDE bound, not a vendor fact. A server
// that rejects a batch this size surfaces as an http_<code> LLMError
// rather than being pre-empted here. If a cited cap is ever found, cite it
// in this same comment and keep the marker. It deliberately does not reuse
// the Voyage arm's cap, which is a Voyage fact.
const geminiBatchSize = 64

// geminiDtypeFloat32 is the ONE representation this arm can serve, spelled
// as the RESOLVED CONFIG spells it — the same string config.AcceptedEmbedDtypes
// carries. It is a CAPABILITY STATEMENT and not a wire value; see the file
// comment for why this arm has no wire spelling at all.
const geminiDtypeFloat32 = "float32"

type geminiEmbedder struct {
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

// Compile-time assertion: *geminiEmbedder satisfies BinaryEmbedder.
var _ BinaryEmbedder = (*geminiEmbedder)(nil)

func init() { RegisterProvider(ProviderGemini, newGeminiFromConfig) }

// newGeminiFromConfig is the registered factory. It REFUSES any dtype
// other than geminiDtypeFloat32.
func newGeminiFromConfig(_ context.Context, cfg *Config) (BinaryEmbedder, error) {
	if cfg == nil {
		return nil, errNilConfig()
	}
	if err := geminiDtypeRefusal(cfg.Dtype); err != nil {
		return nil, err
	}
	e := &geminiEmbedder{
		APIKey:    cfg.APIKey,
		BaseURL:   cfg.BaseURL,
		Model:     cfg.Model,
		Dimension: cfg.Dimension,
		Dtype:     cfg.Dtype,
		InputRole: cfg.InputRole,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
	if e.BaseURL == "" {
		e.BaseURL = geminiDefaultBaseURL
	}
	if e.Model == "" {
		// Through defaultModelFor so the model sent and the model
		// ResolveIdentity states are one value. See identity.go.
		e.Model = defaultModelFor(ProviderGemini)
	}
	if e.InputRole == "" {
		e.InputRole = InputRoleDocument
	}
	return e, nil
}

// geminiDtypeRefusal is the arm's one capability statement, shared by the
// factory and the methods so a directly-constructed instance cannot hand
// back a representation it never agreed to serve.
//
// THE REFUSAL NAMES BOTH SIDES — the value it refused AND the value it can
// serve — following the unknownEmbedProviderError shape in package config: an
// operator who lands here must not have to guess the spelling that works. A
// refusal that named only the bad value is how a legitimate configuration
// becomes indistinguishable from a typo.
func geminiDtypeRefusal(dtype string) error {
	if dtype == geminiDtypeFloat32 {
		return nil
	}
	return fmt.Errorf("%w: the gemini arm cannot produce dtype %q (accepted: %s) — Gemini publishes no quantized embedding output for its embedding models", ErrInvalidConfig, dtype, geminiDtypeFloat32)
}

// geminiTaskType maps the semantic InputRole onto GEMINI'S OWN vocabulary
// — a THIRD distinct spelling, after Voyage's document/query and Cohere's
// search_document/search_query. Each arm owns its mapping; no arm reads
// another's.
func (e *geminiEmbedder) geminiTaskType() string {
	if e.InputRole == InputRoleQuery {
		return "RETRIEVAL_QUERY"
	}
	return "RETRIEVAL_DOCUMENT"
}

// geminiEmbedRequest is one :batchEmbedContents body. The dimension knob
// is outputDimensionality on the wire.
type geminiEmbedRequest struct {
	Requests []geminiEmbedOne `json:"requests"`
}

type geminiEmbedOne struct {
	Model                string        `json:"model"`
	Content              geminiContent `json:"content"`
	TaskType             string        `json:"task_type,omitempty"`
	OutputDimensionality int           `json:"outputDimensionality,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

// geminiEmbedResponse decodes float values, NOT the int narrowing the
// Voyage arm uses: a float response run through that narrowing would be
// truncated to garbage.
type geminiEmbedResponse struct {
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
}

// Available reports whether this arm has something to authenticate with.
func (e *geminiEmbedder) Available() bool {
	return e.APIKey != "" || e.BaseURL != geminiDefaultBaseURL
}

// EmbedBinary generates one embedding at the configured model, width and
// dtype, by delegating to the batch path so both share one request builder.
func (e *geminiEmbedder) EmbedBinary(ctx context.Context, text string) ([]byte, error) {
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
// cap, posts one call per pack, and encodes each returned float row through
// encodeFloat32LE. Results preserve input order.
//
// THE PACKING IS NOT A CHOICE MADE HERE. Four little-endian bytes per value is
// the v3 segment's float view's own contract, stated once in encodeFloat32LE
// and read by the same format the Voyage arm's float32 rows are sealed into.
// This arm asks that function rather than deciding for itself, which is what
// the earlier refusal here was holding out for.
//
// THE DTYPE IS RE-CHECKED, not only gated at the factory, because every test in
// this package constructs an arm directly and so bypasses the factory: an
// instance carrying a dtype this arm never agreed to serve must refuse rather
// than emit float bytes under a label claiming something else.
func (e *geminiEmbedder) EmbedBinaryBatch(ctx context.Context, texts []string) ([][]byte, error) {
	if err := geminiDtypeRefusal(e.Dtype); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return nil, nil
	}
	var all [][]byte
	for i, pack := range packGemini(texts) {
		rows, err := e.embedFloatBatch(ctx, pack)
		if err != nil {
			return nil, fmt.Errorf("batch %d: %w", i, err)
		}
		for _, row := range rows {
			all = append(all, encodeFloat32LE(row))
		}
	}
	return all, nil
}

// embedFloatBatch posts one pack to :batchEmbedContents and decodes float
// vectors. It is the arm's real request path, reached from EmbedBinaryBatch
// above once per pack.
func (e *geminiEmbedder) embedFloatBatch(ctx context.Context, texts []string) ([][]float32, error) {
	model := "models/" + e.Model
	reqs := make([]geminiEmbedOne, 0, len(texts))
	for _, t := range texts {
		reqs = append(reqs, geminiEmbedOne{
			Model:                model,
			Content:              geminiContent{Parts: []geminiPart{{Text: t}}},
			TaskType:             e.geminiTaskType(),
			OutputDimensionality: e.Dimension,
		})
	}
	body, err := json.Marshal(geminiEmbedRequest{Requests: reqs})
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "marshal_request", Cause: err}
	}

	url := e.BaseURL + "/v1beta/" + model + ":batchEmbedContents"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "create_request", Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	// A keyless base_url targets a compatible endpoint that handles auth
	// out-of-band, so the header is only sent when a key was resolved.
	if e.APIKey != "" {
		req.Header.Set("x-goog-api-key", e.APIKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, &llm.LLMError{Transient: true, Reason: "network", Cause: fmt.Errorf("gemini embed request: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &llm.LLMError{
			Transient:  llm.HTTPStatusToTransient(resp.StatusCode),
			Reason:     fmt.Sprintf("http_%d", resp.StatusCode),
			RetryAfter: llm.ParseRetryAfter(resp.Header),
			Cause:      fmt.Errorf("gemini embed: status %d: %s", resp.StatusCode, string(respBody)),
		}
	}

	var result geminiEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "decode_response", Cause: err}
	}
	vectors := make([][]float32, len(result.Embeddings))
	for i, d := range result.Embeddings {
		vectors[i] = d.Values
	}
	return vectors, nil
}

// packGemini splits texts into packs bounded by this arm's own item cap.
func packGemini(texts []string) [][]string {
	return packByCount(texts, geminiBatchSize)
}
