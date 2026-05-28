// SPDX-License-Identifier: Apache-2.0

package rerank

import (
	"encoding/json"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// rerank_pipeline_test.go is the runtime-evaluation unit-test surface for
// the rerank pipeline DSL: Predicate.Eval, the per-op Apply paths, and the
// Pipeline.ApplyPre/ApplyPost fan-out. Pure, in-memory, no graph DB, no
// httptest, no gRPC. Synthetic Node + HydratedResult fixtures only.
//
// Parse-time / validation-time tests + the executeSearch hard-error live in
// rerank_pipeline_parse_test.go (split solely to keep both files under the
// 500-line cap). Shared fixture helpers (rawJSON, fixtureNode, resultSpec,
// makeResults) are defined here and used by the sibling file via package
// proximity.

// fixtureNode builds a synthetic Node with every Predicate-readable string
// field populated, plus an inline Metadata map so metadata.<key> reads
// resolve through kgtypes.Value's scalar fallback path (no regHint needed).
func fixtureNode() *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:          "fixture-1",
		Type:        "function",
		SymbolName:  "HandleAuth",
		FilePath:    "pkg/auth/handler.go",
		Summary:     "OIDC token validation middleware for API routes",
		Description: "Validates incoming JWT bearer tokens against the OIDC discovery document",
		Keywords:    "auth oidc jwt token middleware bearer",
		Signature:   "func HandleAuth(ctx context.Context, token string) error",
		Status:      "active",
		Content:     "func HandleAuth(ctx context.Context, token string) error { return nil }",
		Metadata: map[string]string{
			"package": "auth",
			"owner":   "platform",
		},
	}
}

// rawJSON is a tiny helper that turns a value into a json.RawMessage —
// keeps the table-driven leaf tests legible.
func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// validateLeaf builds + validates a leaf Predicate, returning it ready for
// Eval. Mirrors the parse path so eager regex compile populates p.compiled.
func validateLeaf(t *testing.T, field, match string, value any) *Predicate {
	t.Helper()
	p := &Predicate{Field: field, Match: match, Value: rawJSON(t, value)}
	require.NoError(t, p.validate(0))
	return p
}

// TestPredicate_LeafMatchOps exercises every leaf match operator against
// a synthetic Node that carries all the addressable string fields.
func TestPredicate_LeafMatchOps(t *testing.T) {
	n := fixtureNode()

	t.Run("regex matches symbol_name", func(t *testing.T) {
		p := validateLeaf(t, "symbol_name", "regex", "^Handle[A-Z]")
		require.NotNil(t, p.compiled, "regex must be eager-compiled")
		assert.True(t, p.Eval("ignored", n))
	})

	t.Run("prefix on file_path", func(t *testing.T) {
		p := validateLeaf(t, "file_path", "prefix", "pkg/auth/")
		assert.True(t, p.Eval("ignored", n))
		// No match path:
		miss := validateLeaf(t, "file_path", "prefix", "internal/")
		assert.False(t, miss.Eval("ignored", n))
	})

	t.Run("suffix on file_path", func(t *testing.T) {
		p := validateLeaf(t, "file_path", "suffix", "_test.go")
		assert.False(t, p.Eval("ignored", n))
		hit := validateLeaf(t, "file_path", "suffix", "handler.go")
		assert.True(t, hit.Eval("ignored", n))
	})

	t.Run("contains on summary", func(t *testing.T) {
		p := validateLeaf(t, "summary", "contains", "OIDC")
		assert.True(t, p.Eval("ignored", n))
	})

	t.Run("equals on type", func(t *testing.T) {
		p := validateLeaf(t, "type", "equals", "function")
		assert.True(t, p.Eval("ignored", n))
		miss := validateLeaf(t, "type", "equals", "method")
		assert.False(t, miss.Eval("ignored", n))
	})

	t.Run("in on status", func(t *testing.T) {
		p := validateLeaf(t, "status", "in", []string{"pending", "active", "completed"})
		assert.True(t, p.Eval("ignored", n))
		miss := validateLeaf(t, "status", "in", []string{"pending", "blocked"})
		assert.False(t, miss.Eval("ignored", n))
	})

	t.Run("tokens_match on keywords with $query interpolation", func(t *testing.T) {
		// keywords contains "auth oidc jwt ..."; query "OIDC discovery" tokenizes
		// to {"oidc","discovery"}; "oidc" overlaps → match.
		p := validateLeaf(t, "keywords", "tokens_match", "$query")
		assert.True(t, p.Eval("OIDC discovery", n))
		// Disjoint tokens → no match.
		assert.False(t, p.Eval("kubernetes deployment", n))
	})
}

// TestPredicate_MetadataField confirms the metadata.<key> prefix form
// resolves through Node.Value's inline-map fallback (regHint is nil for
// hand-built Nodes, which is the documented test path).
func TestPredicate_MetadataField(t *testing.T) {
	n := fixtureNode()

	t.Run("metadata key resolves via Node.Value", func(t *testing.T) {
		p := validateLeaf(t, "metadata.package", "equals", "auth")
		assert.True(t, p.Eval("ignored", n))
	})

	t.Run("metadata absent key reads empty string", func(t *testing.T) {
		// "team" is not present in fixtureNode; equals "" matches because
		// Node.Value returns "" for absent keys (documented behavior).
		p := validateLeaf(t, "metadata.team", "equals", "")
		assert.True(t, p.Eval("ignored", n))
	})
}

// TestPredicate_BooleanComposition covers any/all/not + Negate, with
// $query interpolation in scalar and []string forms, and depth invariants.
func TestPredicate_BooleanComposition(t *testing.T) {
	n := fixtureNode()

	t.Run("any short-circuits on first true", func(t *testing.T) {
		// First child is true (file_path contains pkg/auth) — second child
		// would be false but is never reached observably; we just confirm
		// the composition returns true.
		p := &Predicate{
			Any: []Predicate{
				{Field: "file_path", Match: "contains", Value: rawJSON(t, "pkg/auth")},
				{Field: "type", Match: "equals", Value: rawJSON(t, "method")},
			},
		}
		require.NoError(t, p.validate(0))
		assert.True(t, p.Eval("ignored", n))
	})

	t.Run("all short-circuits on first false", func(t *testing.T) {
		p := &Predicate{
			All: []Predicate{
				{Field: "type", Match: "equals", Value: rawJSON(t, "method")}, // false
				{Field: "file_path", Match: "contains", Value: rawJSON(t, "pkg/auth")},
			},
		}
		require.NoError(t, p.validate(0))
		assert.False(t, p.Eval("ignored", n))
	})

	t.Run("not inverts child", func(t *testing.T) {
		p := &Predicate{
			Not: &Predicate{Field: "type", Match: "equals", Value: rawJSON(t, "method")},
		}
		require.NoError(t, p.validate(0))
		assert.True(t, p.Eval("ignored", n))
	})

	t.Run("Negate flips a leaf result", func(t *testing.T) {
		p := &Predicate{
			Field: "type", Match: "equals", Value: rawJSON(t, "function"),
			Negate: true,
		}
		require.NoError(t, p.validate(0))
		assert.False(t, p.Eval("ignored", n))
	})

	t.Run("Negate flips a boolean composition once", func(t *testing.T) {
		// any-of-trues = true; Negate → false.
		p := &Predicate{
			Negate: true,
			Any: []Predicate{
				{Field: "type", Match: "equals", Value: rawJSON(t, "function")},
			},
		}
		require.NoError(t, p.validate(0))
		assert.False(t, p.Eval("ignored", n))
	})

	t.Run("depth-3 nesting validates and evaluates", func(t *testing.T) {
		// any{ all{ not{ leaf } } } — 3 levels of boolean composition,
		// terminating in a leaf at depth 3. Validate counts depth at each
		// boolean level; depth==3 must pass.
		p := &Predicate{
			Any: []Predicate{
				{All: []Predicate{
					{Not: &Predicate{Field: "type", Match: "equals", Value: rawJSON(t, "method")}},
				}},
			},
		}
		require.NoError(t, p.validate(0))
		assert.True(t, p.Eval("ignored", n))
	})

	t.Run("depth-4 nesting rejected at validate", func(t *testing.T) {
		// any{ any{ any{ any{ leaf } } } } — depth 4 must be rejected.
		p := &Predicate{
			Any: []Predicate{{Any: []Predicate{{Any: []Predicate{{Any: []Predicate{
				{Field: "type", Match: "equals", Value: rawJSON(t, "function")},
			}}}}}}},
		}
		err := p.validate(0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "depth")
	})

	t.Run("$query interpolates in scalar string value", func(t *testing.T) {
		// summary contains the live query string.
		p := validateLeaf(t, "summary", "contains", "$query")
		assert.True(t, p.Eval("OIDC token", n))
		assert.False(t, p.Eval("kubernetes", n))
	})

	t.Run("$query interpolates per-element of in-array", func(t *testing.T) {
		// status is "active"; in-list contains literal "pending" plus
		// $query → "active" → match.
		p := validateLeaf(t, "status", "in", []string{"pending", "$query"})
		assert.True(t, p.Eval("active", n))
		assert.False(t, p.Eval("blocked", n))
	})

	t.Run("mutual exclusion violation rejected", func(t *testing.T) {
		// Mixing leaf form and boolean form is invalid.
		p := &Predicate{
			Field: "type", Match: "equals", Value: rawJSON(t, "function"),
			Any: []Predicate{
				{Field: "type", Match: "equals", Value: rawJSON(t, "function")},
			},
		}
		err := p.validate(0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot mix")
	})
}

// resultSpec is the per-element shape consumed by makeResults.
type resultSpec struct {
	id    string
	t     kgtypes.NodeType
	path  string
	score float64
}

// makeResults is a SearchResult fixture builder for Apply tests. Each
// element gets a distinct Node ID + score so ordering is observable.
func makeResults(specs ...resultSpec) []engine.SearchResult {
	out := make([]engine.SearchResult, len(specs))
	for i, s := range specs {
		out[i] = engine.SearchResult{
			Node:  &knowledgev1.Node{Id: s.id, Type: string(s.t), FilePath: s.path},
			Score: s.score,
		}
	}
	return out
}

// TestApply_FilterOp covers both action directions. Empty-input
// pass-through is exercised inside the ApplyPre tests below.
func TestApply_FilterOp(t *testing.T) {
	in := makeResults(
		resultSpec{"a", "function", "pkg/foo.go", 3.0},
		resultSpec{"b", "method", "pkg/bar.go", 2.0},
		resultSpec{"c", "function", "pkg/baz_test.go", 1.0},
	)

	t.Run("action=drop removes matches", func(t *testing.T) {
		op := &FilterOp{
			Op:     "filter",
			Action: "drop",
			Where: Predicate{
				Field: "file_path", Match: "suffix", Value: rawJSON(t, "_test.go"),
			},
		}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		require.Len(t, out, 2)
		assert.Equal(t, "a", out[0].Node.Id)
		assert.Equal(t, "b", out[1].Node.Id)
	})

	t.Run("action=keep retains only matches", func(t *testing.T) {
		op := &FilterOp{
			Op:     "filter",
			Action: "keep",
			Where: Predicate{
				Field: "type", Match: "equals", Value: rawJSON(t, "function"),
			},
		}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		require.Len(t, out, 2)
		assert.Equal(t, "a", out[0].Node.Id)
		assert.Equal(t, "c", out[1].Node.Id)
	})
}

// TestApply_ScoreOp covers all three modes plus the set+0.0 corner where
// the zeroed entry sinks under the stable re-sort.
func TestApply_ScoreOp(t *testing.T) {
	in := makeResults(
		resultSpec{"a", "function", "pkg/foo.go", 3.0},
		resultSpec{"b", "method", "pkg/bar.go", 2.0},
		resultSpec{"c", "function", "pkg/baz.go", 1.0},
	)

	matchAFunc := Predicate{Field: "type", Match: "equals", Value: rawJSON(t, "function")}

	t.Run("mode=multiply scales matching scores", func(t *testing.T) {
		op := &ScoreOp{Op: "score", Where: matchAFunc, Mode: "multiply", Value: 0.5}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		require.Len(t, out, 3)
		// Scores: a=1.5, b=2.0, c=0.5 → re-sort: b, a, c.
		assert.Equal(t, "b", out[0].Node.Id)
		assert.InDelta(t, 2.0, out[0].Score, 1e-9)
		assert.Equal(t, "a", out[1].Node.Id)
		assert.InDelta(t, 1.5, out[1].Score, 1e-9)
		assert.Equal(t, "c", out[2].Node.Id)
		assert.InDelta(t, 0.5, out[2].Score, 1e-9)
	})

	t.Run("mode=add bumps matching scores", func(t *testing.T) {
		op := &ScoreOp{Op: "score", Where: matchAFunc, Mode: "add", Value: 10.0}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		// Scores: a=13.0, b=2.0 (no match), c=11.0 → re-sort: a, c, b.
		assert.Equal(t, "a", out[0].Node.Id)
		assert.Equal(t, "c", out[1].Node.Id)
		assert.Equal(t, "b", out[2].Node.Id)
	})

	t.Run("mode=set with value 0.0 sinks matches to bottom", func(t *testing.T) {
		op := &ScoreOp{Op: "score", Where: matchAFunc, Mode: "set", Value: 0.0}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		require.Len(t, out, 3)
		// Scores: a=0, b=2.0, c=0 → re-sort: b first; a and c tied at 0.0,
		// stable sort preserves their original relative order (a before c).
		assert.Equal(t, "b", out[0].Node.Id)
		assert.InDelta(t, 2.0, out[0].Score, 1e-9)
		assert.Equal(t, "a", out[1].Node.Id)
		assert.InDelta(t, 0.0, out[1].Score, 1e-9)
		assert.Equal(t, "c", out[2].Node.Id)
		assert.InDelta(t, 0.0, out[2].Score, 1e-9)
	})

	t.Run("input slice is not mutated", func(t *testing.T) {
		op := &ScoreOp{Op: "score", Where: matchAFunc, Mode: "set", Value: 99.0}
		require.NoError(t, op.Validate())
		_, err := op.Apply("ignored", in)
		require.NoError(t, err)
		// Originals unchanged.
		assert.InDelta(t, 3.0, in[0].Score, 1e-9)
		assert.InDelta(t, 2.0, in[1].Score, 1e-9)
		assert.InDelta(t, 1.0, in[2].Score, 1e-9)
	})
}

// TestApply_LimitOp confirms truncate + pass-through-when-shorter-than-N.
func TestApply_LimitOp(t *testing.T) {
	in := makeResults(
		resultSpec{"a", "function", "pkg/a.go", 5.0},
		resultSpec{"b", "function", "pkg/b.go", 4.0},
		resultSpec{"c", "function", "pkg/c.go", 3.0},
	)

	t.Run("truncates to N", func(t *testing.T) {
		op := &LimitOp{Op: "limit", N: 2}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		require.Len(t, out, 2)
		assert.Equal(t, "a", out[0].Node.Id)
		assert.Equal(t, "b", out[1].Node.Id)
	})

	t.Run("input shorter than N passes through", func(t *testing.T) {
		op := &LimitOp{Op: "limit", N: 10}
		require.NoError(t, op.Validate())
		out, err := op.Apply("ignored", in)
		require.NoError(t, err)
		assert.Len(t, out, 3)
	})

	t.Run("invalid N rejected by Validate", func(t *testing.T) {
		op := &LimitOp{Op: "limit", N: 0}
		require.Error(t, op.Validate())
	})
}

// TestPipeline_ApplyPre covers nil receiver, empty Pre, empty input, and
// the filter-all-of-N hard error message naming the phase.
func TestPipeline_ApplyPre(t *testing.T) {
	in := makeResults(
		resultSpec{"a", "function", "pkg/a_test.go", 3.0},
		resultSpec{"b", "function", "pkg/b_test.go", 2.0},
	)

	t.Run("nil pipeline passes through", func(t *testing.T) {
		var p *Pipeline
		out, err := p.ApplyPre("q", in)
		require.NoError(t, err)
		assert.Equal(t, in, out)
	})

	t.Run("empty Pre passes through", func(t *testing.T) {
		p := &Pipeline{Post: []Op{&LimitOp{Op: "limit", N: 5}}}
		out, err := p.ApplyPre("q", in)
		require.NoError(t, err)
		assert.Equal(t, in, out)
	})

	t.Run("empty input passes through", func(t *testing.T) {
		p := &Pipeline{Pre: []Op{&FilterOp{
			Op:     "filter",
			Action: "drop",
			Where:  Predicate{Field: "type", Match: "equals", Value: rawJSON(t, "function")},
		}}}
		require.NoError(t, p.Validate())
		out, err := p.ApplyPre("q", nil)
		require.NoError(t, err)
		assert.Empty(t, out)
	})

	t.Run("filter-all-of-N hard errors with phase name and count", func(t *testing.T) {
		p := &Pipeline{Pre: []Op{&FilterOp{
			Op:     "filter",
			Action: "drop",
			Where:  Predicate{Field: "file_path", Match: "suffix", Value: rawJSON(t, "_test.go")},
		}}}
		require.NoError(t, p.Validate())
		_, err := p.ApplyPre("q", in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pre")
		assert.Contains(t, err.Error(), "2") // len(in) named in the message
	})
}

// TestPipeline_ApplyPost mirrors ApplyPre but checks the post phase name in
// the error message and confirms the happy path.
func TestPipeline_ApplyPost(t *testing.T) {
	in := makeResults(
		resultSpec{"a", "function", "pkg/a_test.go", 3.0},
	)

	t.Run("filter-all-of-N hard errors with post phase name", func(t *testing.T) {
		p := &Pipeline{Post: []Op{&FilterOp{
			Op:     "filter",
			Action: "drop",
			Where:  Predicate{Field: "file_path", Match: "suffix", Value: rawJSON(t, "_test.go")},
		}}}
		require.NoError(t, p.Validate())
		_, err := p.ApplyPost("q", in)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "post")
	})

	t.Run("happy path threads through ops", func(t *testing.T) {
		p := &Pipeline{Post: []Op{
			&LimitOp{Op: "limit", N: 1},
		}}
		require.NoError(t, p.Validate())
		out, err := p.ApplyPost("q", in)
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, "a", out[0].Node.Id)
	})
}
