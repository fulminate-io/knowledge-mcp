// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRewriteSearchArgs covers the pure rewrite stage of InterceptSearch.
// Strategies expansion sub-cases are preserved from the prior attempt's
// TestRewriteSearchArgs_StrategiesExpansion (Phase 0 manual criterion saved
// the test logic before deletion). The hasReranker cases are new for the
// client-side rerank wiring.
func TestRewriteSearchArgs(t *testing.T) {
	t.Run("hasReranker=false, no strategies_file is passthrough", func(t *testing.T) {
		raw := []byte(`{"text":"foo"}`)
		out, _, hasRewrite, err := rewriteSearchArgs(raw, false)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if hasRewrite {
			t.Errorf("hasRewrite=true, want false when no rewrite applies")
		}
		if string(out) != string(raw) {
			t.Errorf("passthrough mismatch:\n got: %s\nwant: %s", out, raw)
		}
	})

	t.Run("hasReranker=false, valid file expands to verbatim bytes under strategies key", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "strat.json")
		fileBytes := []byte(`{}`)
		if err := os.WriteFile(path, fileBytes, 0o600); err != nil {
			t.Fatalf("write start file: %v", err)
		}
		raw, err := json.Marshal(map[string]any{
			"text":            "foo",
			"strategies_file": path,
		})
		if err != nil {
			t.Fatalf("marshal raw args: %v", err)
		}

		out, _, hasRewrite, err := rewriteSearchArgs(raw, false)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !hasRewrite {
			t.Fatalf("hasRewrite=false, want true on successful expansion")
		}

		var probe map[string]json.RawMessage
		if err := json.Unmarshal(out, &probe); err != nil {
			t.Fatalf("decode expanded args: %v", err)
		}
		if _, present := probe["strategies_file"]; present {
			t.Errorf("strategies_file key leaked into expanded payload: %s", out)
		}
		startBytes, ok := probe["strategies"]
		if !ok {
			t.Fatalf("expanded payload missing strategies key: %s", out)
		}
		if string(startBytes) != string(fileBytes) {
			t.Errorf("strategies bytes != file bytes:\n got: %s\nwant: %s", startBytes, fileBytes)
		}
		var textVal string
		if err := json.Unmarshal(probe["text"], &textVal); err != nil {
			t.Fatalf("decode text field: %v", err)
		}
		if textVal != "foo" {
			t.Errorf("text field mutated: got %q, want %q", textVal, "foo")
		}
	})

	t.Run("hasReranker=false, missing strategies_file errors and names the path", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist.json")
		raw, _ := json.Marshal(map[string]any{"strategies_file": missing})
		out, _, _, err := rewriteSearchArgs(raw, false)
		if err == nil {
			t.Fatalf("expected error for missing file, got nil")
		}
		if out != nil {
			t.Errorf("expected nil output on error, got: %s", out)
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error should name missing path %q, got: %v", missing, err)
		}
	})

	t.Run("hasReranker=false, invalid pipeline returns parse error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(path, []byte(`{"pre":[{"op":"frob"}]}`), 0o600); err != nil {
			t.Fatalf("write bad start file: %v", err)
		}
		raw, _ := json.Marshal(map[string]any{"strategies_file": path})
		out, _, _, err := rewriteSearchArgs(raw, false)
		if err == nil {
			t.Fatalf("expected parse error, got nil")
		}
		if out != nil {
			t.Errorf("expected nil output on error, got: %s", out)
		}
		if !strings.Contains(err.Error(), "unknown op") {
			t.Errorf("error should mention unknown op, got: %v", err)
		}
	})

	t.Run("hasReranker=true widens limit + coerces format and drops fields", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]any{
			"text":   "foo",
			"limit":  20,
			"format": "text",
			"fields": []string{"id", "name"},
		})
		out, saved, hasRewrite, err := rewriteSearchArgs(raw, true)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !hasRewrite {
			t.Fatalf("hasRewrite=false, want true when hasReranker=true")
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(out, &probe); err != nil {
			t.Fatalf("decode rewritten args: %v", err)
		}
		var limOut int
		_ = json.Unmarshal(probe["limit"], &limOut)
		if limOut != widePoolSize {
			t.Errorf("limit not widened: got %d, want %d", limOut, widePoolSize)
		}
		var fmtOut string
		_ = json.Unmarshal(probe["format"], &fmtOut)
		if fmtOut != "json" {
			t.Errorf("format not coerced: got %q, want %q", fmtOut, "json")
		}
		if _, present := probe["fields"]; present {
			t.Errorf("fields not dropped: %s", out)
		}
		if saved.originalLimit != 20 {
			t.Errorf("originalLimit captured wrong: got %d, want 20", saved.originalLimit)
		}
		if saved.originalFormat != "text" {
			t.Errorf("originalFormat captured wrong: got %q, want %q", saved.originalFormat, "text")
		}
		if !saved.clientSideActive {
			t.Errorf("clientSideActive=false, want true")
		}
	})

	t.Run("hasReranker=true combined with strategies_file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "strat.json")
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatalf("write start file: %v", err)
		}
		raw, _ := json.Marshal(map[string]any{
			"text":            "foo",
			"strategies_file": path,
		})
		out, _, hasRewrite, err := rewriteSearchArgs(raw, true)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !hasRewrite {
			t.Fatalf("hasRewrite=false, want true")
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(out, &probe); err != nil {
			t.Fatalf("decode rewritten args: %v", err)
		}
		if _, present := probe["strategies"]; !present {
			t.Errorf("strategies missing after combined rewrite: %s", out)
		}
		var lim int
		_ = json.Unmarshal(probe["limit"], &lim)
		if lim != widePoolSize {
			t.Errorf("limit not widened in combined rewrite: got %d, want %d", lim, widePoolSize)
		}
	})
}

// TestInterceptSearchCode_RoutesViaComposeCodeSearch covers the CLASS-A
// claim: search(graph:code) routes through the SAME composeCodeSearch composer
// (Query→Text via mergeCodeQueries) against the CLIENT engine, with no
// fall-through to the legacy server search-code path (unconditional).
func TestInterceptSearchCode_RoutesViaComposeCodeSearch(t *testing.T) {
	var execHits atomic.Int64
	// The hydrate ids[] read is served by the harness GraphClient; the ranked
	// node is returned as the canned RETURN_MODE_NODES reply.
	gc := newInterceptHarness(t, &execHits, cannedNodesResp(
		&knowledgev1.Node{Id: "f.go:Foo", SymbolName: "Foo", Type: "function", FilePath: "f.go", StartLine: 1},
	))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: "f.go:Foo", Score: 0.9}}}
	deps := &interceptDeps{gc: gc, segMgr: mgr}
	// The search tool carries the query under `query` (not `text`).
	raw := json.RawMessage(`{"graph":"code","query":"foo","repo":"knowledge"}`)
	handled, res := interceptSearchCode(context.Background(), deps, gc.Execute, raw)
	require.True(t, handled, "graph=code must be claimed client-side")
	require.False(t, res.IsError, textBodyTools(res))
	body := textBodyTools(res)
	assert.Equal(t, int64(1), mgr.calls.Load(), "single query → one client Search (no legacy fall-through)")
	assert.Equal(t, kgtypes.GraphCode, mgr.lastGT)
	assert.Equal(t, "knowledge", mgr.lastName, "code engine keyed on repo")
	assert.Contains(t, body, "[knowledge]")
	// no embedder, no caller vector → BM25-only → mode label "text".
	assert.Contains(t, body, `Found 1 results for "foo" (mode: text):`)
	assert.Contains(t, body, "Foo (function)")
}

// TestInterceptSearchCode_NoQueryFallsThrough asserts a graph=code search with
// no query/queries falls through (handled=false) so the caller proceeds.
func TestInterceptSearchCode_NoQueryFallsThrough(t *testing.T) {
	mgr := &fakeSegmentSearcher{}
	deps := &interceptDeps{segMgr: mgr}
	handled, _ := interceptSearchCode(context.Background(), deps, nil, json.RawMessage(`{"graph":"code"}`))
	assert.False(t, handled, "no query → fall through")
	assert.Equal(t, int64(0), mgr.calls.Load(), "no client Search fires when there is no query")
}
