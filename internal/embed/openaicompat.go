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

// The GENERIC OPENAI-COMPATIBLE ARM: one arm for the long tail of servers
// that speak the OpenAI embeddings shape — vLLM, Ollama, Jina, Together,
// Fireworks, OpenRouter, LocalAI, LM Studio and Azure's v1 path. It is the
// arm the keyless base_url rule matters most for, because most of that
// population is a local endpoint that handles auth out-of-band.
//
// IT IS FLOAT-ONLY BY NATURE, not by omission. The response envelope
// converged across providers, but the REQUEST diverged exactly on the
// quantization knobs: this envelope carries no dtype field at all, so
// there is no way to ask a compatible server for a binary vector. The arm
// therefore REFUSES every dtype but the unquantized one at factory time,
// naming the dtype it refused and the one it can serve, rather than
// emitting something wrong. Do NOT add a client-side binarizer: which
// encoding a float vector takes on the way to bytes, and whether the index
// reader agrees, belongs to the float-native vector index's contract, not
// to a choice made silently here.
//
// TWO SPELLINGS LIVE IN THIS FILE AND THEY ARE NOT THE SAME STRING. The
// CONFIG vocabulary spells the unquantized representation "float32"
// (config.AcceptedEmbedDtypes) and that is the only spelling an operator can
// put in [embedder]; the WIRE vocabulary spells this envelope's
// encoding_format "float", which is what a compatible server parses. One
// literal was doing both jobs, so every dtype the build admitted was refused
// by this arm while the arm's only accepted dtype was refused by
// Config.Validate before the factory ran — the arm was unconstructible under
// every configuration an operator could write, and nothing was red until a
// test intersected the two sets (see
// TestEveryRegisteredProvider_ConstructsAtSomeAdmittedDtype). They are now
// two named constants, openAICompatDtypeFloat32 and
// openAICompatWireEncodingFormat, precisely so a change to one cannot silently
// move the other.
//
// THE FLOAT-TO-BYTES QUESTION IS ANSWERED, and not here: four little-endian
// bytes per value is the v3 segment float view's own contract, stated once in
// encodeFloat32LE and read by the same format the Voyage arm's float32 rows
// are sealed into. EmbedBinaryBatch asks that function rather than choosing a
// packing of its own, which is what the refusal this file used to return in
// its place was holding out for.

// openAICompatDefaultBaseURL is the endpoint this arm posts to when the
// resolved config supplies no base_url.
const openAICompatDefaultBaseURL = "https://api.openai.com"

// openAICompatDefaultModel is the model this arm uses when the resolved
// config supplies an empty model.
const openAICompatDefaultModel = "text-embedding-3-small"

// openaiCompatBatchSize is the max texts per request for this arm.
//
// cap provenance: NONE IS DOCUMENTED. No per-request item cap is published
// across the OpenAI-compatible server population this arm serves, so this
// value is a conservative CLIENT-SIDE bound, not a vendor fact. A server
// that rejects a batch this size surfaces as an http_<code> LLMError
// rather than being pre-empted here. It deliberately does not reuse the
// Voyage arm's item cap: that number is a Voyage fact, and sharing it
// would let one provider's limit govern another's.
const openaiCompatBatchSize = 64

// openAICompatDtypeFloat32 is the ONE representation this arm can serve,
// spelled as the RESOLVED CONFIG spells it — the same string
// config.AcceptedEmbedDtypes carries. It is a CAPABILITY STATEMENT and is
// deliberately NOT the value that goes on the wire.
const openAICompatDtypeFloat32 = "float32"

// openAICompatWireEncodingFormat is what this arm puts in the request's
// encoding_format field. It is the OPENAI EMBEDDINGS VOCABULARY, a different
// set from the config one above, and it stays whatever a compatible server
// parses regardless of how the config spells the representation. Conflating
// the two is the defect the file comment describes.
const openAICompatWireEncodingFormat = "float"

type openAICompatEmbedder struct {
	APIKey    string
	BaseURL   string
	Model     string
	Dimension int
	// Dtype is the representation this instance agreed to serve. It is
	// carried on the struct, rather than only checked at the factory, so the
	// methods can re-check it: every test in this package constructs an arm
	// directly, bypassing the factory entirely.
	Dtype  string
	client *http.Client
}

// Compile-time assertion: *openAICompatEmbedder satisfies BinaryEmbedder.
var _ BinaryEmbedder = (*openAICompatEmbedder)(nil)

func init() { RegisterProvider(ProviderOpenAICompatible, newOpenAICompatFromConfig) }

// newOpenAICompatFromConfig is the registered factory. It REFUSES any dtype
// other than openAICompatDtypeFloat32, naming the configured dtype and the one
// it can serve.
func newOpenAICompatFromConfig(_ context.Context, cfg *Config) (BinaryEmbedder, error) {
	if cfg == nil {
		return nil, errNilConfig()
	}
	if err := openAICompatDtypeRefusal(cfg.Dtype); err != nil {
		return nil, err
	}
	e := &openAICompatEmbedder{
		APIKey:    cfg.APIKey,
		BaseURL:   cfg.BaseURL,
		Model:     cfg.Model,
		Dimension: cfg.Dimension,
		Dtype:     cfg.Dtype,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
	if e.BaseURL == "" {
		e.BaseURL = openAICompatDefaultBaseURL
	}
	if e.Model == "" {
		// Through defaultModelFor so the model sent and the model
		// ResolveIdentity states are one value. See identity.go.
		e.Model = defaultModelFor(ProviderOpenAICompatible)
	}
	return e, nil
}

// openAICompatDtypeRefusal is the arm's one capability statement, shared
// by the factory and by the methods so a directly-constructed instance
// cannot hand back a representation it never agreed to serve.
//
// THE REFUSAL NAMES BOTH SIDES — the value it refused AND the value it can
// serve — following the unknownEmbedProviderError shape in package config: an
// operator who lands here must not have to guess the spelling that works. A
// refusal that named only the bad value is how a legitimate configuration
// becomes indistinguishable from a typo.
func openAICompatDtypeRefusal(dtype string) error {
	if dtype == openAICompatDtypeFloat32 {
		return nil
	}
	return fmt.Errorf("%w: the generic openai-compatible arm cannot produce dtype %q (accepted: %s) — the OpenAI embeddings request envelope carries no quantization knob, so there is no way to ask a compatible server for a quantized vector", ErrInvalidConfig, dtype, openAICompatDtypeFloat32)
}

// openAICompatRequest is the OpenAI embeddings request envelope.
//
// The role has NO EQUIVALENT KNOB here: this envelope has no input_type or
// task_type field, so the arm IGNORES InputRole rather than inventing a
// field for it. That is stated rather than silently dropped.
type openAICompatRequest struct {
	Input          []string `json:"input"`
	Model          string   `json:"model"`
	EncodingFormat string   `json:"encoding_format"`
	Dimensions     int      `json:"dimensions,omitempty"`
}

// openAICompatResponse is the converged response envelope. Embedding is
// decoded as []float32 and NOT as []int: the Voyage arm's decode narrows
// each element to a byte, which would truncate a float response to
// garbage. A separate response type is the point of a separate arm, not
// duplication.
type openAICompatResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Available reports whether this arm has something to authenticate with.
// A keyless base_url is valid, so either one suffices.
func (e *openAICompatEmbedder) Available() bool {
	return e.APIKey != "" || e.BaseURL != ""
}

// EmbedBinary generates one embedding at the configured model, width and
// dtype, by delegating to the batch path so both share one request builder.
func (e *openAICompatEmbedder) EmbedBinary(ctx context.Context, text string) ([]byte, error) {
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
// THE PACKING IS NOT A CHOICE MADE HERE — see the file comment: it is the v3
// segment float view's contract, stated once and shared with the Voyage arm.
//
// THE DTYPE IS RE-CHECKED, not only gated at the factory, because every test in
// this package constructs an arm directly and so bypasses the factory: an
// instance carrying a dtype this arm never agreed to serve must refuse rather
// than emit float bytes under a label claiming something else.
func (e *openAICompatEmbedder) EmbedBinaryBatch(ctx context.Context, texts []string) ([][]byte, error) {
	if err := openAICompatDtypeRefusal(e.Dtype); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return nil, nil
	}
	var all [][]byte
	for i, pack := range packOpenAICompat(texts) {
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

// embedFloatBatch posts one pack and decodes float vectors. It is the arm's
// real request path, reached from EmbedBinaryBatch above once per pack.
//
// encoding_format carries the WIRE constant, never the config dtype: the two
// vocabularies are different sets and this line is where conflating them put a
// config spelling in front of a compatible server.
func (e *openAICompatEmbedder) embedFloatBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(openAICompatRequest{
		Input:          texts,
		Model:          e.Model,
		EncodingFormat: openAICompatWireEncodingFormat,
		Dimensions:     e.Dimension,
	})
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "marshal_request", Cause: err}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.BaseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "create_request", Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, &llm.LLMError{Transient: true, Reason: "network", Cause: fmt.Errorf("openai-compatible embed request: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &llm.LLMError{
			Transient:  llm.HTTPStatusToTransient(resp.StatusCode),
			Reason:     fmt.Sprintf("http_%d", resp.StatusCode),
			RetryAfter: llm.ParseRetryAfter(resp.Header),
			Cause:      fmt.Errorf("openai-compatible embed: status %d: %s", resp.StatusCode, string(respBody)),
		}
	}

	var result openAICompatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "decode_response", Cause: err}
	}
	vectors := make([][]float32, len(result.Data))
	for i, d := range result.Data {
		vectors[i] = d.Embedding
	}
	return vectors, nil
}

// packOpenAICompat splits texts into packs bounded by this arm's own item
// cap. Bounded by item count only: no total-batch token cap is documented
// for this server population, so a server-side rejection surfaces as an
// http_<code> LLMError rather than being pre-empted.
func packOpenAICompat(texts []string) [][]string {
	return packByCount(texts, openaiCompatBatchSize)
}

// packByCount is the shared item-count packer. Each arm supplies its OWN
// cap — the packing loop is common, the number never is.
func packByCount(texts []string, size int) [][]string {
	if len(texts) == 0 || size <= 0 {
		return nil
	}
	out := make([][]string, 0, (len(texts)+size-1)/size)
	for start := 0; start < len(texts); start += size {
		end := min(start+size, len(texts))
		out = append(out, texts[start:end])
	}
	return out
}
