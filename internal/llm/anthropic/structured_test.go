// SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// summarizerShapedSchema mirrors the live summarizer schema: object root,
// items array, and the numeric keywords the native path must strip
// (maxLength/minItems/maxItems).
const summarizerShapedSchema = `{"type":"object","properties":{` +
	`"items":{"type":"array","items":{"type":"object","properties":{` +
	`"summary":{"type":"string","maxLength":200},` +
	`"keywords":{"type":"array","items":{"type":"string"},"minItems":3,"maxItems":15}` +
	`},"required":["summary","keywords"],"additionalProperties":false},"minItems":20,"maxItems":20}` +
	`},"required":["items"],"additionalProperties":false}`

// TestStructuredOutputWireShape is the load-bearing fails-when-absent wire
// contract: native models marshal output_config (with a stripped schema, no
// tool_choice key), older models marshal the forced-tool fallback (tools +
// tool_choice, no output_config). Reverting the Phase 2 applyResponseFormat
// call makes the native sub-test go red (req.OutputConfig nil).
func TestStructuredOutputWireShape(t *testing.T) {
	rf := llm.WithResponseFormat(&llm.ResponseFormat{
		Type:   "json_schema",
		Schema: json.RawMessage(summarizerShapedSchema),
	})
	okResp := `{"id":"m","type":"message","role":"assistant","model":"x",` +
		`"content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":1,"output_tokens":1}}`

	t.Run("native model → output_config, stripped schema, no tool_choice", func(t *testing.T) {
		srv, cap := newFakeServer(t, 200, okResp)
		svc := withClient(t, srv, "claude-haiku-4-5")
		if _, err := svc.Generate(context.Background(),
			[]*schema.Message{{Role: schema.User, Content: "hi"}}, rf); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		var sent anthropicRequest
		if err := json.Unmarshal(cap.body, &sent); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if sent.OutputConfig == nil {
			t.Fatal("OutputConfig nil — applyResponseFormat native path not wired")
		}
		if sent.OutputConfig.Format.Type != "json_schema" {
			t.Errorf("Format.Type = %q, want json_schema", sent.OutputConfig.Format.Type)
		}
		schemaStr := string(sent.OutputConfig.Format.Schema)
		if !strings.Contains(schemaStr, `"additionalProperties":false`) {
			t.Errorf("additionalProperties:false missing from native schema: %s", schemaStr)
		}
		for _, kw := range []string{"maxLength", "minItems", "maxItems"} {
			if strings.Contains(schemaStr, kw) {
				t.Errorf("keyword %q not stripped from native schema: %s", kw, schemaStr)
			}
		}
		if len(sent.Tools) != 0 {
			t.Errorf("native path should leave Tools empty, got %+v", sent.Tools)
		}
		// Raw-key absence (T3-1): catches an accidental empty-but-present key.
		if bytes.Contains(cap.body, []byte("tool_choice")) {
			t.Errorf("native request must contain NO tool_choice key: %s", cap.body)
		}
	})

	t.Run("fallback model → tools + tool_choice, no output_config", func(t *testing.T) {
		srv, cap := newFakeServer(t, 200, okResp)
		svc := withClient(t, srv, "claude-3-haiku-20240307")
		if _, err := svc.Generate(context.Background(),
			[]*schema.Message{{Role: schema.User, Content: "hi"}}, rf); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		var sent anthropicRequest
		if err := json.Unmarshal(cap.body, &sent); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if sent.OutputConfig != nil {
			t.Errorf("fallback path must leave OutputConfig nil, got %+v", sent.OutputConfig)
		}
		if len(sent.Tools) != 1 || sent.Tools[0].Name != "structured_output" {
			t.Fatalf("fallback should append the structured_output tool, got %+v", sent.Tools)
		}
		if !bytes.Contains(cap.body, []byte("tool_choice")) {
			t.Fatalf("fallback request must contain a tool_choice key: %s", cap.body)
		}
		var tc struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(sent.ToolChoice, &tc); err != nil {
			t.Fatalf("unmarshal tool_choice: %v", err)
		}
		if tc.Type != "tool" || tc.Name != "structured_output" {
			t.Errorf("tool_choice = %+v, want {tool, structured_output}", tc)
		}
	})
}

// structuredRF is the json_schema ResponseFormat option used across the
// structured-output tests, mirroring the summarizer's schema shape (object
// root, items array, the numeric keywords the native path strips).
func structuredRF() llm.Option {
	schema := json.RawMessage(`{"type":"object","properties":{` +
		`"items":{"type":"array","items":{"type":"object","properties":{` +
		`"summary":{"type":"string","maxLength":200},` +
		`"keywords":{"type":"array","items":{"type":"string"},"minItems":3,"maxItems":15}` +
		`},"required":["summary","keywords"],"additionalProperties":false},"minItems":1,"maxItems":1}` +
		`},"required":["items"],"additionalProperties":false}`)
	return llm.WithResponseFormat(&llm.ResponseFormat{Type: "json_schema", Schema: schema})
}

// itemsPayload mirrors the summarizer's decoded JSON shape, used to confirm a
// fixture body round-trips into the structure the consumer expects.
type itemsPayload struct {
	Items []struct {
		Summary  string   `json:"summary"`
		Keywords []string `json:"keywords"`
	} `json:"items"`
}

// TestStructuredOutputResponseFixtures proves both wire shapes round-trip
// end-to-end through the existing parse path with no parse-side change: a
// native content[0].text JSON body lands in resp.Content, and a tool_use input
// lands in resp.ToolCalls[0].Function.Arguments — both as the same parseable
// JSON.
func TestStructuredOutputResponseFixtures(t *testing.T) {
	const itemsJSON = `{"items":[{"summary":"s","keywords":["a","b","c"]}]}`

	t.Run("native content[0].text JSON → resp.Content parses", func(t *testing.T) {
		srv, _ := newFakeServer(t, 200, `{
			"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5",
			"content":[{"type":"text","text":`+strconv.Quote(itemsJSON)+`}],
			"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
		}`)
		svc := withClient(t, srv, "claude-haiku-4-5")
		resp, err := svc.Generate(context.Background(),
			[]*schema.Message{{Role: schema.User, Content: "hi"}}, structuredRF())
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if resp.Content != itemsJSON {
			t.Errorf("Content = %q, want %q", resp.Content, itemsJSON)
		}
		var p itemsPayload
		if err := json.Unmarshal([]byte(resp.Content), &p); err != nil {
			t.Fatalf("Content does not unmarshal into items shape: %v", err)
		}
		if len(p.Items) != 1 || p.Items[0].Summary != "s" {
			t.Errorf("decoded items = %+v, want one item summary=s", p.Items)
		}
	})

	t.Run("tool_use input → resp.ToolCalls[0].Function.Arguments parses", func(t *testing.T) {
		srv, _ := newFakeServer(t, 200, `{
			"id":"m","type":"message","role":"assistant","model":"claude-3-haiku-20240307",
			"content":[{"type":"tool_use","id":"t1","name":"structured_output","input":`+itemsJSON+`}],
			"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}
		}`)
		svc := withClient(t, srv, "claude-3-haiku-20240307")
		resp, err := svc.Generate(context.Background(),
			[]*schema.Message{{Role: schema.User, Content: "hi"}}, structuredRF())
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if len(resp.ToolCalls) != 1 {
			t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
		}
		args := resp.ToolCalls[0].Function.Arguments
		if args != itemsJSON {
			t.Errorf("Arguments = %q, want %q", args, itemsJSON)
		}
		var p itemsPayload
		if err := json.Unmarshal([]byte(args), &p); err != nil {
			t.Fatalf("Arguments do not unmarshal into items shape: %v", err)
		}
		if len(p.Items) != 1 || p.Items[0].Keywords[0] != "a" {
			t.Errorf("decoded items = %+v, want one item keyword[0]=a", p.Items)
		}
	})
}

// TestStructuredOutputFallbackBridge covers the three response-shape cases of
// the Generate-level content bridge on the forced-tool fallback path.
func TestStructuredOutputFallbackBridge(t *testing.T) {
	const gated = "claude-3-haiku-20240307" // non-native → fallback path

	t.Run("ResponseFormat + structured_output tool_use → Content filled, ToolCalls kept", func(t *testing.T) {
		input := `{"items":[{"summary":"s","keywords":["a","b","c"]}]}`
		srv, _ := newFakeServer(t, 200, `{
			"id":"m","type":"message","role":"assistant","model":"`+gated+`",
			"content":[{"type":"tool_use","id":"t1","name":"structured_output","input":`+input+`}],
			"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}
		}`)
		svc := withClient(t, srv, gated)
		resp, err := svc.Generate(context.Background(),
			[]*schema.Message{{Role: schema.User, Content: "hi"}}, structuredRF())
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if resp.Content != input {
			t.Errorf("Content = %q, want the tool_use input JSON %q", resp.Content, input)
		}
		if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "structured_output" {
			t.Errorf("ToolCalls should be preserved, got %+v", resp.ToolCalls)
		}
	})

	t.Run("genuine WithTools tool_use (other name, no ResponseFormat) → Content stays empty", func(t *testing.T) {
		srv, _ := newFakeServer(t, 200, `{
			"id":"m","type":"message","role":"assistant","model":"`+gated+`",
			"content":[{"type":"tool_use","id":"t1","name":"my_real_tool","input":{"a":1}}],
			"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}
		}`)
		svc := withClient(t, srv, gated)
		// No ResponseFormat → bridge must not fire at all.
		resp, err := svc.Generate(context.Background(),
			[]*schema.Message{{Role: schema.User, Content: "hi"}})
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if resp.Content != "" {
			t.Errorf("Content = %q, want empty (no ResponseFormat → no bridge)", resp.Content)
		}
		if len(resp.ToolCalls) != 1 {
			t.Errorf("genuine tool_use should remain in ToolCalls, got %+v", resp.ToolCalls)
		}
	})

	t.Run("text block + structured_output tool_use → real text kept (bridge skipped)", func(t *testing.T) {
		srv, _ := newFakeServer(t, 200, `{
			"id":"m","type":"message","role":"assistant","model":"`+gated+`",
			"content":[{"type":"text","text":"real answer"},{"type":"tool_use","id":"t1","name":"structured_output","input":{"items":[]}}],
			"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}
		}`)
		svc := withClient(t, srv, gated)
		resp, err := svc.Generate(context.Background(),
			[]*schema.Message{{Role: schema.User, Content: "hi"}}, structuredRF())
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if resp.Content != "real answer" {
			t.Errorf("Content = %q, want \"real answer\" (bridge must not clobber a real text block)", resp.Content)
		}
	})
}

// TestStructuredOutputLoudErrors is the never-silent contract: every
// un-honorable ResponseFormat returns a non-nil error whose message NAMES the
// offending cause. A silent pass-through (the old behavior) would return a nil
// error with the format dropped — these cases would then go red.
func TestStructuredOutputLoudErrors(t *testing.T) {
	const okResp = `{"id":"m","type":"message","role":"assistant","model":"x",` +
		`"content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":1,"output_tokens":1}}`

	cases := []struct {
		name      string
		model     llm.Model
		opts      []llm.Option
		wantToken string
	}{
		{
			name:  "unrenderable keyword oneOf",
			model: "claude-haiku-4-5",
			opts: []llm.Option{llm.WithResponseFormat(&llm.ResponseFormat{
				Type:   "json_schema",
				Schema: json.RawMessage(`{"type":"object","properties":{"x":{"oneOf":[]}}}`),
			})},
			wantToken: "oneOf",
		},
		{
			name:  "non-object schema root",
			model: "claude-haiku-4-5",
			opts: []llm.Option{llm.WithResponseFormat(&llm.ResponseFormat{
				Type:   "json_schema",
				Schema: json.RawMessage(`{"type":"array"}`),
			})},
			wantToken: "object",
		},
		{
			name:  "extended thinking on non-native model",
			model: "claude-3-haiku-20240307",
			opts: []llm.Option{
				llm.WithResponseFormat(&llm.ResponseFormat{
					Type:   "json_schema",
					Schema: json.RawMessage(summarizerShapedSchema),
				}),
				llm.WithExtendedThinking(2048),
			},
			wantToken: "thinking",
		},
		{
			name:  "non-json_schema format type",
			model: "claude-haiku-4-5",
			opts: []llm.Option{llm.WithResponseFormat(&llm.ResponseFormat{
				Type:   "json_object",
				Schema: json.RawMessage(`{"type":"object"}`),
			})},
			wantToken: "json_object",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newFakeServer(t, 200, okResp)
			svc := withClient(t, srv, tc.model)
			_, err := svc.Generate(context.Background(),
				[]*schema.Message{{Role: schema.User, Content: "hi"}}, tc.opts...)
			if err == nil {
				t.Fatalf("expected a loud error naming %q, got nil (silent drop)", tc.wantToken)
			}
			if !strings.Contains(err.Error(), tc.wantToken) {
				t.Errorf("error %q does not name %q", err.Error(), tc.wantToken)
			}
		})
	}
}

// TestTruncation covers the max_tokens truncation sentinel: a structured
// request that stops on max_tokens returns the exported, wrappable truncation
// error; a non-structured max_tokens response does not.
func TestTruncation(t *testing.T) {
	const maxTokensResp = `{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5",` +
		`"content":[{"type":"text","text":"{\"items\":["}],"stop_reason":"max_tokens",` +
		`"usage":{"input_tokens":1,"output_tokens":1}}`

	t.Run("structured + max_tokens → truncation sentinel", func(t *testing.T) {
		srv, _ := newFakeServer(t, 200, maxTokensResp)
		svc := withClient(t, srv, "claude-haiku-4-5")
		_, err := svc.Generate(context.Background(),
			[]*schema.Message{{Role: schema.User, Content: "hi"}}, structuredRF())
		if err == nil {
			t.Fatal("expected a truncation error")
		}
		if !errors.Is(err, ErrResponseTruncated) {
			t.Errorf("errors.Is(ErrResponseTruncated) = false for %v", err)
		}
		if _, ok := errors.AsType[*TruncatedOutputError](err); !ok {
			t.Errorf("errors.As(*TruncatedOutputError) = false for %v", err)
		}
		var le *llm.LLMError
		if !errors.As(err, &le) {
			t.Fatalf("expected *llm.LLMError, got %v", err)
		}
		if le.Reason != "response_truncated" {
			t.Errorf("Reason = %q, want response_truncated", le.Reason)
		}
	})

	t.Run("non-structured + max_tokens → no truncation error", func(t *testing.T) {
		srv, _ := newFakeServer(t, 200, maxTokensResp)
		svc := withClient(t, srv, "claude-haiku-4-5")
		resp, err := svc.Generate(context.Background(),
			[]*schema.Message{{Role: schema.User, Content: "hi"}})
		if err != nil {
			t.Fatalf("non-structured max_tokens should not error: %v", err)
		}
		if resp.FinishReason != llm.FinishReasonMaxTokens {
			t.Errorf("FinishReason = %q, want max_tokens", resp.FinishReason)
		}
	})
}

// TestRenderNativeSchema is the unit table over the native schema normalizer.
// The verdict case (modeling the supervisor verdictSchema) is the regression
// guard: minimum/maximum must be stripped while enum is PRESERVED, so an
// accidental edit to the strip list (dropping minimum/maximum) or an
// over-strip that removes enum goes red here.
func TestRenderNativeSchema(t *testing.T) {
	cases := []struct {
		name         string
		schema       string
		wantStrip    []string // tokens that must be absent after rendering
		wantPreserve []string // tokens that must survive
	}{
		{
			name:         "summarizer schema strips numeric keywords, keeps additionalProperties",
			schema:       summarizerShapedSchema,
			wantStrip:    []string{"maxLength", "minItems", "maxItems"},
			wantPreserve: []string{`"additionalProperties":false`, `"items"`, `"keywords"`},
		},
		{
			name: "verdict schema strips minimum/maximum, preserves enum",
			schema: `{"type":"object","properties":{` +
				`"state":{"type":"string","enum":["working","done","stuck","off-rails"]},` +
				`"confidence":{"type":"number","minimum":0,"maximum":1},` +
				`"reason":{"type":"string"},` +
				`"result":{"type":"string"}` +
				`},"required":["state","confidence","reason","result"],"additionalProperties":false}`,
			wantStrip:    []string{"minimum", "maximum"},
			wantPreserve: []string{`"enum"`, "working", "off-rails", `"additionalProperties":false`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := renderNativeSchema(json.RawMessage(tc.schema))
			if err != nil {
				t.Fatalf("renderNativeSchema: %v", err)
			}
			got := string(out)
			for _, tok := range tc.wantStrip {
				if strings.Contains(got, tok) {
					t.Errorf("keyword %q not stripped: %s", tok, got)
				}
			}
			for _, tok := range tc.wantPreserve {
				if !strings.Contains(got, tok) {
					t.Errorf("expected %q preserved, missing from: %s", tok, got)
				}
			}
		})
	}
}
