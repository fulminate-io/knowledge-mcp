// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// ErrResponseTruncated is the exported sentinel marking a structured-output
// response that the model truncated at its max_tokens cap — the partial JSON
// body is unparseable. Exported and wrappable so a downstream breaker /
// pipeline classifier can errors.Is it and treat truncation distinctly from a
// generic parse failure.
var ErrResponseTruncated = errors.New("anthropic: structured output truncated at max_tokens")

// TruncatedOutputError is the typed truncation error returned (wrapped in an
// *llm.LLMError) from Generate when a structured-output request stops on
// max_tokens. It wraps ErrResponseTruncated so errors.Is(err,
// ErrResponseTruncated) matches, and is itself errors.As-matchable so callers
// can read StopReason / MaxTokens to size a retry with a larger budget.
type TruncatedOutputError struct {
	// StopReason is the provider stop_reason that triggered truncation
	// (always "max_tokens" today; recorded for diagnostics).
	StopReason string
	// MaxTokens is the request's max_tokens cap that was hit, so a caller
	// retrying can raise it.
	MaxTokens int
}

// Error implements error with guidance to raise max_tokens.
func (e *TruncatedOutputError) Error() string {
	return fmt.Sprintf("%v (stop_reason=%s, max_tokens=%d); raise max_tokens and retry", ErrResponseTruncated, e.StopReason, e.MaxTokens)
}

// Unwrap exposes ErrResponseTruncated so errors.Is matches the sentinel.
func (e *TruncatedOutputError) Unwrap() error { return ErrResponseTruncated }

// nativeStripKeywords are JSON Schema keywords that Anthropic's native
// output_config does NOT support. They are stripped UNCONDITIONALLY at every
// nesting level (the strip is value-blind, not keep-if-0-or-1): native accepts
// minItems/maxItems only as 0 or 1, but the live summary schema uses 20/20
// (un-renderable), so strip-all is chosen over a value-aware conditional until
// a second consumer schema actually needs the 0/1 case. These are
// belt-and-suspenders tightening only — count drift is already caught by the
// summarizer's len(parsed.Items) guard plus the max_tokens truncation sentinel.
var nativeStripKeywords = map[string]struct{}{
	"maxLength":  {},
	"minLength":  {},
	"minItems":   {},
	"maxItems":   {},
	"minimum":    {},
	"maximum":    {},
	"multipleOf": {},
	"pattern":    {},
	"format":     {},
}

// nativeRejectKeywords are JSON Schema keywords that Anthropic's native
// output_config cannot represent at all (composition / reference). Their
// presence anywhere in the schema tree is a LOUD translate error naming the
// keyword — we never silently drop a constraint that changes the accepted
// value set.
var nativeRejectKeywords = map[string]struct{}{
	"oneOf": {},
	"anyOf": {},
	"allOf": {},
	"not":   {},
	"$ref":  {},
}

// anthropicOutputConfig mirrors the native Structured Outputs request knob
// on /v1/messages: {"output_config":{"format":{"type":"json_schema",
// "schema":<schema>}}}. Anthropic's GA Structured Outputs feature constrains
// the model to a JSON schema natively (no synthesized tool); this is the
// primary structured-output path for 4.5+/5 models.
type anthropicOutputConfig struct {
	Format anthropicOutputFormat `json:"format"`
}

// anthropicOutputFormat is the format block inside output_config. Type is
// the format identifier ("json_schema"); Schema is the rendered, native-safe
// JSON schema body produced by renderNativeSchema.
type anthropicOutputFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

// applyResponseFormat translates options.ResponseFormat into either the native
// output_config knob (4.5+/5 models) or the forced-tool_use fallback (older
// models), or returns a LOUD error when the request cannot be honored — it
// never silently drops a ResponseFormat. buildRequest propagates the error as
// the translate failure.
//
// The model gate reads the RESOLVED model arg, NOT options.Model: buildRequest
// receives the model already resolved by pickModel (which may have substituted
// the service default when options.Model is empty), so passing options.Model
// here would mis-gate a default-model request. The resolved model is the model
// actually being called.
//
// Dispatch:
//   - ResponseFormat == nil → no-op.
//   - ResponseFormat.Type != "json_schema" → loud error naming the type.
//   - native model → render the native-safe schema and set req.OutputConfig;
//     Tools/ToolChoice are left untouched. Native + extended thinking coexist,
//     so there is no thinking conflict on this path.
//   - non-native model + extended thinking (not disabled) → loud error: the
//     forced-tool fallback conflicts with extended thinking and native isn't
//     available on this model.
//   - non-native model → append buildOutputTool to req.Tools and pin
//     req.ToolChoice to {"type":"tool","name":"structured_output"}; OutputConfig
//     stays nil.
//
// WithTools + ResponseFormat coexistence disposition: ResponseFormat owns the
// structured-output path. A caller passing both gets output_config on the
// native path (the caller's tools are left as-is) or a force-overridden
// tool_choice on the fallback path (the synthesized tool is appended and the
// model is pinned to it). No current caller combines them — the summarizer and
// supervisor pass ResponseFormat only; the dream adapter passes WithTools only.
func applyResponseFormat(req *anthropicRequest, model llm.Model, options *llm.RequestOptions) error {
	if options == nil || options.ResponseFormat == nil {
		return nil
	}
	rf := options.ResponseFormat
	if rf.Type != "json_schema" {
		return &llm.LLMError{Transient: false, Reason: "translate_request", Cause: fmt.Errorf("anthropic: unsupported ResponseFormat type %q (only json_schema is supported)", rf.Type)}
	}

	if nativeStructuredOutputSupported(model) {
		cleaned, err := renderNativeSchema(rf.Schema)
		if err != nil {
			return err
		}
		req.OutputConfig = &anthropicOutputConfig{
			Format: anthropicOutputFormat{Type: "json_schema", Schema: cleaned},
		}
		return nil
	}

	// Fallback (non-native model): forced tool_use. Extended thinking cannot
	// coexist with a forced tool_choice on these models, and native isn't
	// available — so this combo is genuinely un-honorable.
	if options.ExtendedThinking && !options.DisableExtendedThinking {
		return &llm.LLMError{Transient: false, Reason: "translate_request", Cause: fmt.Errorf("anthropic: structured output on model %q requires the forced-tool fallback, which conflicts with extended thinking; use a 4.5+ model for native structured output", model)}
	}

	schema, err := marshalSchema(rf.Schema)
	if err != nil {
		return err
	}
	req.Tools = append(req.Tools, buildOutputTool(json.RawMessage(schema)))
	req.ToolChoice = json.RawMessage(fmt.Sprintf(`{"type":"tool","name":%q}`, outputToolName))
	return nil
}

// outputToolName is the fixed name of the synthesized tool the forced-tool
// fallback path uses to carry structured output. The response-side content
// bridge (in Generate) finds the structured output by matching this name, so
// it never mistakes a genuine WithTools tool_use for the synthesized one.
const outputToolName = "structured_output"

// buildOutputTool synthesizes the forced tool that carries structured output
// on the fallback (non-native) path. Its input_schema is the caller's
// shape-normalized schema verbatim — standard JSON Schema, so unlike the
// native output_config path it does NOT strip the numeric keywords
// (maxLength/minItems/maxItems): a tool input_schema accepts them, so the
// fallback keeps the tighter constraints. The forced tool_choice that pins the
// model to this tool is set by applyResponseFormat, not here.
//
// Mirrors the proven agent-repo buildWorkflowOutputTool idiom, adapted to our
// hand-rolled anthropicTool{Name, Description, InputSchema json.RawMessage}.
func buildOutputTool(schema json.RawMessage) anthropicTool {
	return anthropicTool{
		Name:        outputToolName,
		Description: "Return the response as structured output matching the provided JSON Schema. Call this tool exactly once with arguments matching the schema.",
		InputSchema: schema,
	}
}

// nativeStructuredOutputSupported reports whether model accepts the native
// output_config Structured Outputs knob. Native Structured Outputs is GA on
// the Claude 4.5+/5 generation (Haiku/Sonnet/Opus 4.5 and later); older
// models (and any non-claude / unparseable name) fall back to the
// forced-tool_use path, which works on every tool-capable model.
//
// We version-parse rather than allowlist: configured models in the wild
// already span claude-haiku-4-5-20251001, claude-haiku-4-5, claude-sonnet-4-6,
// and claude-opus-4-7, and an allowlist rots as new model names ship. The
// parse encodes the 4.5+ boundary durably.
//
// Model names take the shape claude-<family>-<major>[-<minor>][-<date>]
// (e.g. claude-haiku-4-5-20251001 → major 4, minor 5; claude-sonnet-4-6 →
// 4/6; bare claude-haiku-5 → 5/0). The legacy form claude-3-haiku-20240307
// puts the major version immediately after the claude prefix → major 3, which
// correctly resolves to non-native. We locate the version by scanning for the
// first numeric segment after "claude" (the major) and the next numeric
// segment (the minor, defaulting to 0).
func nativeStructuredOutputSupported(model llm.Model) bool {
	segments := strings.Split(string(model), "-")
	if len(segments) == 0 || segments[0] != "claude" {
		return false
	}

	major, minor, ok := -1, 0, false
	for i := 1; i < len(segments); i++ {
		n, err := strconv.Atoi(segments[i])
		if err != nil {
			continue
		}
		if !ok {
			// First numeric segment is the major version. A date suffix
			// (e.g. 20240307) only appears AFTER a version, so the first
			// numeric segment is always the major.
			major, ok = n, true
			continue
		}
		// Second numeric segment is the minor version — unless it is a
		// date suffix (>= 1000, no real Claude minor reaches that). Treat
		// a large number as a date and stop scanning.
		if n < 1000 {
			minor = n
		}
		break
	}
	if !ok {
		return false
	}
	return (major == 4 && minor >= 5) || major >= 5
}

// marshalSchema normalizes the shapes a caller might hand us via
// ResponseFormat.Schema (json.RawMessage, []byte, string, or an arbitrary
// Go value) into canonical JSON bytes. Anthropic owns its own local copy of
// this idiom — claudecli/marshalJSONSchema and openai/marshalSchema each keep
// theirs; AGENTS.md forbids a shared hand-written package, so we do not hoist
// it. Returns a loud *llm.LLMError when an arbitrary value fails to marshal.
func marshalSchema(s any) ([]byte, error) {
	if s == nil {
		return []byte(`{}`), nil
	}
	switch v := s.(type) {
	case json.RawMessage:
		return []byte(v), nil
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "translate_request", Cause: fmt.Errorf("anthropic: marshal json schema: %w", err)}
	}
	return raw, nil
}

// renderNativeSchema turns a caller's ResponseFormat.Schema into the
// native-safe schema bytes for output_config.format.schema. It runs entirely
// in the anthropic translate layer so the SHARED summarizer/supervisor schema
// is left untouched (claudecli/openai/codex/gemini keep passing their own
// conformance).
//
// Three responsibilities:
//   - Normalize the input shape (marshalSchema) and require a JSON object root
//     with "type":"object" — a non-object root is a loud error.
//   - HARD-REJECT structurally-unrenderable keywords (oneOf/anyOf/allOf/not/
//     $ref) anywhere in the tree, returning a loud error that NAMES the
//     offending keyword. We never silently drop a constraint that changes the
//     accepted value set.
//   - STRIP the unsupported numeric/string-constraint keywords
//     (nativeStripKeywords) recursively — the strip is UNCONDITIONAL and
//     value-blind (see nativeStripKeywords' doc). additionalProperties:false is
//     PRESERVED (native requires it).
func renderNativeSchema(raw any) (json.RawMessage, error) {
	bytes, err := marshalSchema(raw)
	if err != nil {
		return nil, err
	}
	var root any
	if err := json.Unmarshal(bytes, &root); err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "translate_request", Cause: fmt.Errorf("anthropic: native structured output: parse schema: %w", err)}
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, &llm.LLMError{Transient: false, Reason: "translate_request", Cause: fmt.Errorf("anthropic: native structured output requires a JSON object schema root, got %T", root)}
	}
	if t, _ := obj["type"].(string); t != "object" {
		return nil, &llm.LLMError{Transient: false, Reason: "translate_request", Cause: fmt.Errorf("anthropic: native structured output requires a root \"type\":\"object\", got %q", t)}
	}
	if err := stripNativeUnsupported(obj); err != nil {
		return nil, err
	}
	cleaned, err := json.Marshal(obj)
	if err != nil {
		return nil, &llm.LLMError{Transient: false, Reason: "translate_request", Cause: fmt.Errorf("anthropic: native structured output: re-marshal schema: %w", err)}
	}
	return cleaned, nil
}

// stripNativeUnsupported walks a decoded JSON value, rejecting any
// nativeRejectKeyword (loud, names the keyword) and deleting every
// nativeStripKeyword in place. Recurses through object values and array
// elements so the rules apply at every nesting level.
func stripNativeUnsupported(node any) error {
	switch v := node.(type) {
	case map[string]any:
		for key := range nativeRejectKeywords {
			if _, present := v[key]; present {
				return &llm.LLMError{Transient: false, Reason: "translate_request", Cause: fmt.Errorf("anthropic: native structured output cannot render schema keyword %q", key)}
			}
		}
		for key := range nativeStripKeywords {
			delete(v, key)
		}
		for _, child := range v {
			if err := stripNativeUnsupported(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := stripNativeUnsupported(child); err != nil {
				return err
			}
		}
	}
	return nil
}
