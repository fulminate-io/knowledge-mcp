// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/session"

	"connectrpc.com/connect"

	"github.com/cloudwego/eino/schema"
)

// testCatalog returns a small client-owned MCP tool catalog the
// BuildAllowedTools tests filter by allowlist. T-GTB4: the catalog is
// client-owned (no GetToolSchemas RPC), so the tests construct the schema
// literals directly rather than scripting a server stub.
func testCatalog() []kgtools.MCPTool {
	return []kgtools.MCPTool{
		{
			Name:        "search",
			Description: "find code",
			InputSchema: kgtools.InputSchema{
				Type:       "object",
				Properties: map[string]kgtools.Property{"q": {Type: "string", Description: "query"}},
				Required:   []string{"q"},
			},
		},
		{
			Name:        "think",
			Description: "record a thought",
			InputSchema: kgtools.InputSchema{
				Type:       "object",
				Properties: map[string]kgtools.Property{"content": {Type: "string"}},
			},
		},
		{Name: "unwanted", Description: "should not appear"},
	}
}

// capturingDispatch is a test DispatchFunc that records the worker session_id
// stamped onto the ctx (InvokableRun stamps it before dispatching) + the tool
// name + args, and returns a scripted ToolResult / error. It stands in for the
// standard client dispatch path the worker now routes through (T-GTB6 D8).
type capturingDispatch struct {
	gotSession, gotTool, gotArgs string
	result                       kgtools.ToolResult
	err                          error
}

func (c *capturingDispatch) fn() DispatchFunc {
	return func(ctx context.Context, name string, args json.RawMessage) (kgtools.ToolResult, error) {
		c.gotSession = session.SessionIDFromContext(ctx)
		c.gotTool = name
		c.gotArgs = string(args)
		return c.result, c.err
	}
}

// TestMcpTool_InvokableRunStampsSessionAndConcatContent is the core behavioral
// test: InvokableRun routes through the standard dispatch path, (a) stamping
// worker:<name> on the ctx session_id (so the chokepoint Origin-tags events) and
// (b) returning concatenated text from a multi-block result.
func TestMcpTool_InvokableRunStampsSessionAndConcatContent(t *testing.T) {
	cap := &capturingDispatch{result: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: "first"}, {Type: "text", Text: "second"}},
	}}
	tool := &mcpTool{name: "search", schemaInfo: &schema.ToolInfo{Name: "search"}, sessionID: "worker:smoke-hello", dispatch: cap.fn()}
	out, err := tool.InvokableRun(context.Background(), `{"q":"foo"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if out != "first\nsecond" {
		t.Errorf("output = %q, want %q", out, "first\nsecond")
	}
	if cap.gotSession != "worker:smoke-hello" {
		t.Errorf("session = %v, want worker:smoke-hello", cap.gotSession)
	}
	if cap.gotTool != "search" {
		t.Errorf("tool = %v, want search", cap.gotTool)
	}
	if cap.gotArgs != `{"q":"foo"}` {
		t.Errorf("args = %q, want %q", cap.gotArgs, `{"q":"foo"}`)
	}
}

// TestMcpTool_InvokableRunIsErrorReturnsError shows an IsError=true result
// surfaces as a Go error containing the text — eino must see "tool failed"
// rather than a happy-path tool message holding an error string.
func TestMcpTool_InvokableRunIsErrorReturnsError(t *testing.T) {
	cap := &capturingDispatch{result: kgtools.ToolResult{
		IsError: true,
		Content: []kgtools.ContentBlock{{Type: "text", Text: "not authorized"}},
	}}
	tool := &mcpTool{name: "manage", schemaInfo: &schema.ToolInfo{Name: "manage"}, sessionID: "worker:x", dispatch: cap.fn()}
	out, err := tool.InvokableRun(context.Background(), `{}`)
	if err == nil {
		t.Fatalf("InvokableRun err = nil, want error")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Errorf("err = %v, want to contain %q", err, "not authorized")
	}
	if out != "" {
		t.Errorf("output = %q, want empty on error", out)
	}
}

// TestMcpTool_InvokableRunRPCError verifies transport / RPC errors come back
// wrapped with the tool name.
func TestMcpTool_InvokableRunRPCError(t *testing.T) {
	cap := &capturingDispatch{err: connect.NewError(connect.CodeInternal, errors.New("boom"))}
	tool := &mcpTool{name: "search", schemaInfo: &schema.ToolInfo{Name: "search"}, sessionID: "worker:x", dispatch: cap.fn()}
	if _, err := tool.InvokableRun(context.Background(), `{}`); err == nil {
		t.Fatalf("InvokableRun err = nil, want error")
	} else if !strings.Contains(err.Error(), "mcp tool search") {
		t.Errorf("err = %v, want to contain %q", err, "mcp tool search")
	}
}

// TestBuildAllowedTools_HappyPath filters the client-owned catalog by allowlist,
// constructs the mcpTool set, and verifies every returned tool is wired with the
// worker session ID — and that the session round-trips to the dispatch closure.
func TestBuildAllowedTools_HappyPath(t *testing.T) {
	cap := &capturingDispatch{result: kgtools.ToolResult{
		Content: []kgtools.ContentBlock{{Type: "text", Text: "ok"}},
	}}

	tools, err := BuildAllowedTools(testCatalog(), []string{"search", "think"}, "worker:smoke-hello", cap.fn())
	if err != nil {
		t.Fatalf("BuildAllowedTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}

	// Verify each returned tool exposes the right schema info, in allowlist order.
	wantNames := []string{"search", "think"}
	for i, tool := range tools {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		if info.Name != wantNames[i] {
			t.Errorf("tool[%d].Name = %q, want %q", i, info.Name, wantNames[i])
		}
	}

	// Issue one call through the first tool; verify session_id round-trips
	// onto the dispatch ctx.
	out, err := tools[0].InvokableRun(context.Background(), `{"q":"x"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if out != "ok" {
		t.Errorf("output = %q, want ok", out)
	}
	if cap.gotSession != "worker:smoke-hello" {
		t.Errorf("session = %v, want worker:smoke-hello", cap.gotSession)
	}
}

// TestBuildAllowedTools_UnknownTool guards against silent typos in the
// allowlist. Misspelled names abort construction, not run-time.
func TestBuildAllowedTools_UnknownTool(t *testing.T) {
	_, err := BuildAllowedTools(testCatalog(), []string{"search", "notreal"}, "worker:x", (&capturingDispatch{}).fn())
	if err == nil {
		t.Fatalf("BuildAllowedTools: want error for missing tool, got nil")
	}
	if !strings.Contains(err.Error(), "notreal") {
		t.Errorf("err = %v, want to mention %q", err, "notreal")
	}
}

// TestBuildAllowedTools_Validation guards the nil-dispatch and empty-session
// checks. (The catalog-fetch RPC and its nil-client guard are gone — T-GTB4
// made the catalog client-owned.)
func TestBuildAllowedTools_Validation(t *testing.T) {
	if _, err := BuildAllowedTools(testCatalog(), []string{"search"}, "worker:x", nil); err == nil {
		t.Errorf("nil dispatch: want error, got nil")
	}
	if _, err := BuildAllowedTools(testCatalog(), []string{"search"}, "", (&capturingDispatch{}).fn()); err == nil {
		t.Errorf("empty session: want error, got nil")
	}
}

// TestBuildAllowedTools_EmptyAllowlist returns an empty slice so the caller
// sees a clean signal.
func TestBuildAllowedTools_EmptyAllowlist(t *testing.T) {
	tools, err := BuildAllowedTools(testCatalog(), nil, "worker:x", (&capturingDispatch{}).fn())
	if err != nil {
		t.Fatalf("BuildAllowedTools: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("len(tools) = %d, want 0", len(tools))
	}
}

// TestMcpToolToToolInfo_RoundTrip exercises the schema converter directly
// — verifies parameter types, required flags, and array element decoding.
func TestMcpToolToToolInfo_RoundTrip(t *testing.T) {
	info, err := mcpToolToToolInfo(kgtools.MCPTool{
		Name:        "search",
		Description: "find code",
		InputSchema: kgtools.InputSchema{
			Type: "object",
			Properties: map[string]kgtools.Property{
				"q":     {Type: "string", Description: "query"},
				"limit": {Type: "integer", Description: "max results"},
				"tags":  {Type: "array", Items: &kgtools.Property{Type: "string"}},
			},
			Required: []string{"q"},
		},
	})
	if err != nil {
		t.Fatalf("mcpToolToToolInfo: %v", err)
	}
	if info.Name != "search" || info.Desc != "find code" {
		t.Errorf("info = {%q, %q}, want {search, find code}", info.Name, info.Desc)
	}
	if info.ParamsOneOf == nil {
		t.Fatalf("ParamsOneOf = nil, want populated")
	}
	js, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("ToJSONSchema: %v", err)
	}
	// Re-marshal the eino jsonschema and assert the required flag survives.
	out, err := json.Marshal(js)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), `"required":["q"]`) {
		t.Errorf("rendered schema = %s, want to contain required:[q]", out)
	}
}

// TestMcpToolToToolInfo_NoArgs handles tools that take no parameters.
// ParamsOneOf must stay nil so eino correctly reports a no-args tool.
func TestMcpToolToToolInfo_NoArgs(t *testing.T) {
	info, err := mcpToolToToolInfo(kgtools.MCPTool{
		Name:        "ping",
		Description: "ping the server",
		// No properties — substrate represents a no-args tool this way.
		InputSchema: kgtools.InputSchema{Type: "object"},
	})
	if err != nil {
		t.Fatalf("mcpToolToToolInfo: %v", err)
	}
	if info.ParamsOneOf != nil {
		t.Errorf("ParamsOneOf = %v, want nil for no-args tool", info.ParamsOneOf)
	}
}

// TestMcpToolToToolInfo_RejectsNonObject guards against schemas with a
// non-object root, which the substrate doesn't emit and shouldn't accept.
func TestMcpToolToToolInfo_RejectsNonObject(t *testing.T) {
	_, err := mcpToolToToolInfo(kgtools.MCPTool{
		Name: "weird",
		InputSchema: kgtools.InputSchema{
			Type:       "array",
			Properties: map[string]kgtools.Property{"x": {Type: "string"}},
		},
	})
	if err == nil {
		t.Errorf("err = nil, want error for non-object root")
	}
}

// TestJoinTextBlocks_NewlineSeparator checks the multi-block concat shape.
func TestJoinTextBlocks_NewlineSeparator(t *testing.T) {
	blocks := []kgtools.ContentBlock{
		{Type: "text", Text: "alpha"},
		{Type: "text", Text: "beta"},
		{Type: "image", Text: "(skipped)"},
		{Type: "text", Text: "gamma"},
	}
	if got, want := joinTextBlocks(blocks), "alpha\nbeta\ngamma"; got != want {
		t.Errorf("joinTextBlocks = %q, want %q", got, want)
	}
}
