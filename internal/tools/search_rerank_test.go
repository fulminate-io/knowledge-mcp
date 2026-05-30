// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/rerank"
)

// stubReranker satisfies rerank.Reranker for TestApplyClientRerank cases.
// Implementations are inline anonymous closures via newStubReranker so
// individual test cases can supply per-case behavior.
type stubReranker struct {
	fn func(ctx context.Context, q string, in []engine.SearchResult) ([]engine.SearchResult, error)
}

func (s stubReranker) Rerank(ctx context.Context, q string, in []engine.SearchResult) ([]engine.SearchResult, error) {
	return s.fn(ctx, q, in)
}

func newStubReranker(fn func(context.Context, string, []engine.SearchResult) ([]engine.SearchResult, error)) rerank.Reranker {
	return stubReranker{fn: fn}
}

func TestApplyClientRerank(t *testing.T) {
	t.Run("clientSideActive=false is passthrough", func(t *testing.T) {
		original := kgtools.TextResult(`{"query":"q","total":0,"results":[]}`)
		out := applyClientRerank(context.Background(), original, savedState{clientSideActive: false}, newStubReranker(nil))
		if got := engine.FirstTextContent(out); got != engine.FirstTextContent(original) {
			t.Errorf("expected passthrough, got %q", got)
		}
	})

	t.Run("nil reranker is passthrough", func(t *testing.T) {
		original := kgtools.TextResult(`{"query":"q","total":0,"results":[]}`)
		out := applyClientRerank(context.Background(), original, savedState{clientSideActive: true}, nil)
		if got := engine.FirstTextContent(out); got != engine.FirstTextContent(original) {
			t.Errorf("expected passthrough, got %q", got)
		}
	})

	t.Run("resp.IsError is passthrough", func(t *testing.T) {
		original := kgtools.ToolResult{
			IsError: true,
			Content: []kgtools.ContentBlock{{Type: "text", Text: "boom"}},
		}
		out := applyClientRerank(context.Background(), original, savedState{clientSideActive: true}, newStubReranker(nil))
		if !out.IsError {
			t.Errorf("expected error passthrough, got non-error result")
		}
	})

	t.Run("happy path reorders + re-renders text", func(t *testing.T) {
		// Server returns two results: a (0.9), b (0.5). Stub reranker
		// reverses order: b (1.0), a (0.5). Re-rendered text MUST list b first.
		env := engine.SearchJSONResponse{
			Query: "q",
			Total: 2,
			Results: []engine.SearchJSONResult{
				{ID: "a", Name: "A", Type: "finding", Score: 0.9},
				{ID: "b", Name: "B", Type: "finding", Score: 0.5},
			},
		}
		body, _ := json.Marshal(env)
		resp := kgtools.TextResult(string(body))
		rev := newStubReranker(func(_ context.Context, _ string, in []engine.SearchResult) ([]engine.SearchResult, error) {
			out := make([]engine.SearchResult, len(in))
			for i, h := range in {
				out[len(in)-1-i] = h
			}
			return out, nil
		})
		saved := savedState{
			originalLimit:    10,
			originalFormat:   "text",
			originalQuery:    "q",
			clientSideActive: true,
		}
		out := applyClientRerank(context.Background(), resp, saved, rev)
		got := engine.FirstTextContent(out)
		idxA := strings.Index(got, "[finding] A")
		idxB := strings.Index(got, "[finding] B")
		if idxA < 0 || idxB < 0 {
			t.Fatalf("rendered text missing A or B: %s", got)
		}
		if idxB > idxA {
			t.Errorf("expected B before A after rerank, got:\n%s", got)
		}
		// The success render of a client rerank is unconditionally vector+rerank.
		if !strings.Contains(got, "_search mode: vector+rerank_") {
			t.Errorf("expected vector+rerank footer, got:\n%s", got)
		}
	})

	t.Run("projection in saved replays fields client-side", func(t *testing.T) {
		env := engine.SearchJSONResponse{
			Query: "q",
			Total: 1,
			Results: []engine.SearchJSONResult{
				{ID: "a", Name: "A", Type: "finding", Score: 0.9, FilePath: "x.go", Line: 42},
			},
		}
		body, _ := json.Marshal(env)
		resp := kgtools.TextResult(string(body))
		noop := newStubReranker(func(_ context.Context, _ string, in []engine.SearchResult) ([]engine.SearchResult, error) {
			return in, nil
		})
		saved := savedState{
			originalLimit:    10,
			originalFormat:   "json",
			originalFields:   []string{"id", "file_path", "line"},
			originalQuery:    "q",
			clientSideActive: true,
		}
		out := applyClientRerank(context.Background(), resp, saved, noop)
		var probe map[string]json.RawMessage
		if err := json.Unmarshal([]byte(engine.FirstTextContent(out)), &probe); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		var results []map[string]any
		if err := json.Unmarshal(probe["results"], &results); err != nil {
			t.Fatalf("decode results: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("want 1 result, got %d", len(results))
		}
		if _, present := results[0]["name"]; present {
			t.Errorf("projection leaked unrequested field: %v", results[0])
		}
		if results[0]["id"] != "a" {
			t.Errorf("id missing or wrong: %v", results[0])
		}
		if results[0]["file_path"] != "x.go" {
			t.Errorf("file_path missing or wrong: %v", results[0])
		}
	})

	t.Run("reranker error silent-degrades", func(t *testing.T) {
		env := engine.SearchJSONResponse{
			Query: "q", Total: 1,
			Results: []engine.SearchJSONResult{{ID: "a", Score: 0.5}},
		}
		body, _ := json.Marshal(env)
		resp := kgtools.TextResult(string(body))
		fail := newStubReranker(func(_ context.Context, _ string, _ []engine.SearchResult) ([]engine.SearchResult, error) {
			return nil, errors.New("rerank boom")
		})
		saved := savedState{
			originalLimit:    10,
			originalFormat:   "json",
			originalQuery:    "q",
			clientSideActive: true,
		}
		out := applyClientRerank(context.Background(), resp, saved, fail)
		if engine.FirstTextContent(out) != string(body) {
			t.Errorf("expected unchanged passthrough on rerank failure, got %q", engine.FirstTextContent(out))
		}
	})

	t.Run("metadata round-trip preserves callers key", func(t *testing.T) {
		// T3-4 criterion: server-side augmentCallerHints populates the
		// "callers" metadata key; if hydrateFromJSON drops Metadata, the
		// rerank package's renderForRerank silently scores text-only.
		env := engine.SearchJSONResponse{
			Query: "q", Total: 1,
			Results: []engine.SearchJSONResult{
				{ID: "a", Name: "A", Type: "finding", Score: 0.9, Metadata: map[string]string{"callers": "a, b, c"}},
			},
		}
		body, _ := json.Marshal(env)
		hydrated, err := engine.HydrateFromJSON(string(body))
		if err != nil {
			t.Fatalf("HydrateFromJSON: %v", err)
		}
		if len(hydrated) != 1 {
			t.Fatalf("want 1 hydrated, got %d", len(hydrated))
		}
		if got := kgtypes.Value(hydrated[0].Node, "callers"); got != "a, b, c" {
			t.Errorf("callers metadata lost: got %q, want %q", got, "a, b, c")
		}
	})
}
