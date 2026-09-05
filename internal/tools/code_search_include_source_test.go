// SPDX-License-Identifier: Apache-2.0

// code_search_include_source_test.go — include_source on the code arm's json
// branch, and the contradictory-input refusal it forms with `fields`.
//
// include_source was consumed on the TEXT path alone: the json branch returned
// ahead of the render call that reads it, so a caller asking for no source got
// the body anyway. The flag is documented as suppressing source, so honoring it
// on the json branch is the fix and the schema stays as it is — `content` and
// `source` are separately persisted node fields (proto Node: content = 8,
// source = 12), not two spellings of one.

package tools

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// codeBodyNode is codeSeedNode plus a Content body — the thing include_source
// suppresses. Its own function so every arm below measures the same seed.
func codeBodyNode() *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: "f.go:Foo", SymbolName: "Foo", Type: "function", FilePath: "f.go", StartLine: 1,
		Content: "func Foo() { return }",
	}
}

const codeBodyText = "func Foo() { return }"

// TestCodeSearchIncludeSourceOnJSONBranch is R7a. The base case needs its
// SAME-RUN CONTROL: an empty content key is indistinguishable from a seed that
// never carried a body, so the identical call with include_source:true runs
// beside it and must return the body.
func TestCodeSearchIncludeSourceOnJSONBranch(t *testing.T) {
	body := func(t *testing.T, raw string) string {
		t.Helper()
		handled, res := interceptSearchCode(opCtx(), codeSeedDeps(t, codeBodyNode()), nil, json.RawMessage(raw))
		require.True(t, handled)
		require.False(t, res.IsError, textBodyTools(res))
		return textBodyTools(res)
	}

	t.Run("include_source:false leaves no source text in any key", func(t *testing.T) {
		out := body(t, `{"graph":"code","query":"foo","repo":"knowledge","format":"json","include_source":false}`)
		rows := projectedRows(t, out)
		require.Len(t, rows, 1)
		assert.NotContains(t, rows[0], "content", "the body carrier is dropped")
		assert.NotContains(t, rows[0], "source", "the sibling body carrier is dropped with it")
		assert.NotContains(t, out, codeBodyText, "no key smuggles the body through")
		// The row is still a row: suppressing the body must not empty the result.
		assert.Equal(t, "f.go:Foo", rows[0]["id"])
	})

	t.Run("same-run control: include_source:true returns the body", func(t *testing.T) {
		out := body(t, `{"graph":"code","query":"foo","repo":"knowledge","format":"json","include_source":true}`)
		rows := projectedRows(t, out)
		require.Len(t, rows, 1)
		assert.Equal(t, codeBodyText, rows[0]["content"],
			"without this the suppressed case proves nothing — an absent body would look identical")
	})

	t.Run("same-run control: an absent flag still returns the body", func(t *testing.T) {
		out := body(t, `{"graph":"code","query":"foo","repo":"knowledge","format":"json"}`)
		rows := projectedRows(t, out)
		require.Len(t, rows, 1)
		assert.Equal(t, codeBodyText, rows[0]["content"],
			"an absent include_source resolves to true, so the default call is unchanged")
	})

	t.Run("the projected route honors it too", func(t *testing.T) {
		out := body(t, `{"graph":"code","query":"foo","repo":"knowledge","format":"json","include_source":false,"fields":["id","summary"]}`)
		rows := projectedRows(t, out)
		require.Len(t, rows, 1)
		assert.Equal(t, []string{"id", "summary"}, rowKeys(rows[0]))
		assert.NotContains(t, out, codeBodyText)
	})

	t.Run("the multi-repo render site honors it as well", func(t *testing.T) {
		withTestManifest(t)
		gc := newFanOutHarness(t, []string{"repoA"},
			&knowledgev1.Node{Id: "a.go:A", SymbolName: "A", Type: "function", FilePath: "a.go", StartLine: 1,
				Content: codeBodyText},
		)
		mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
			"repoA": {{ID: "a.go:A", Score: 0.90}},
		})
		handled, res := interceptSearchCode(opCtx(), &interceptDeps{gc: gc, segMgr: mgr}, nil,
			json.RawMessage(`{"graph":"code","query":"x","repos":["repoA"],"format":"json","include_source":false}`))
		require.True(t, handled)
		require.False(t, res.IsError, textBodyTools(res))
		out := textBodyTools(res)
		rows := projectedRows(t, out)
		require.Len(t, rows, 1)
		assert.NotContains(t, rows[0], "content", "the cross-repo json site drops the body too")
		assert.NotContains(t, out, codeBodyText)
	})
}

// TestCodeSearchIncludeSourceDoesNotMutateTheHydratedNode is the implementation
// hazard this change has to dodge: engine.SearchResult.Node is a
// *knowledgev1.Node that flattenCodeResults copies straight out of the hydrated
// CodeResolvedResult, so clearing the body IN PLACE would blank a node other
// readers share. Driven through the sub-composer with the in-process fake, whose
// hydrate hands back the very pointers it holds — over the http harness the node
// is decoded fresh per call and the mutation would be invisible.
func TestCodeSearchIncludeSourceDoesNotMutateTheHydratedNode(t *testing.T) {
	node := codeBodyNode()
	f := &codeSearchEngineFake{
		hitsByRepo: map[string][]searchengine.Hit{"knowledge": {{ID: "f.go:Foo", Score: 0.9}}},
		nodes:      map[string]*knowledgev1.Node{"f.go:Foo": node},
	}
	cdeps := cdepsFor(f)
	cdeps.degrade = &searchDegrade{}
	res := composeCodeSearchSingleRepo(context.Background(), nil, cdeps,
		codeSearchArgs{Graph: "code", Repo: "knowledge", Text: "foo", Format: "json"},
		[]string{"foo"}, nil, 10, false, false)
	require.False(t, res.IsError, textBodyTools(res))

	rows := projectedRows(t, textBodyTools(res))
	require.Len(t, rows, 1)
	assert.NotContains(t, rows[0], "content", "the rendered row carries no body")
	assert.Equal(t, codeBodyText, node.GetContent(),
		"the SHARED hydrated node keeps its body — the render suppresses, it does not blank the node")
}

// TestCodeSearchIncludeSourceContradictsContentProjection is R7b: a call that
// suppresses the source AND names `content` in its projection is contradictory
// input, and contradictory input is refused naming BOTH parameters rather than
// letting either silently win. The rule, in the user's words: "bad input always
// errors".
//
// THE REFUSAL IS A CONJUNCTION, so both single-variable controls run beside it:
// a gate that fired on either half alone would be a different gate and would
// fail neither assertion below on its own.
func TestCodeSearchIncludeSourceContradictsContentProjection(t *testing.T) {
	call := func(t *testing.T, args map[string]any) kgtools.ToolResult {
		t.Helper()
		base := map[string]any{"graph": "code", "query": "foo", "repo": "knowledge"}
		maps.Copy(base, args)
		raw, err := json.Marshal(base)
		require.NoError(t, err)
		handled, res := interceptSearchCode(opCtx(), codeSeedDeps(t, codeBodyNode()), nil, raw)
		require.True(t, handled, "a refused call is still a claimed call, not a fall-through")
		return res
	}

	t.Run("refused, naming both parameters", func(t *testing.T) {
		res := call(t, map[string]any{
			"format": "json", "include_source": false, "fields": []string{"content"},
		})
		require.True(t, res.IsError, "contradictory input must error: %s", textBodyTools(res))
		msg := textBodyTools(res)
		assert.Contains(t, msg, "include_source", "the refusal names the suppression flag")
		assert.Contains(t, msg, "fields", "the refusal names the projection")
		assert.Contains(t, msg, "content", "the refusal names the key that collides")
	})

	// The gate sits ahead of the single/multi dispatch and therefore ahead of both
	// sub-composers' format branches, so the text arm is refused on the same
	// terms. Pinned rather than assumed.
	t.Run("refused on the text render path too", func(t *testing.T) {
		res := call(t, map[string]any{"include_source": false, "fields": []string{"content"}})
		require.True(t, res.IsError, "the refusal is about the input, not the render format: %s", textBodyTools(res))
		assert.Contains(t, textBodyTools(res), "include_source")
	})

	t.Run("control: include_source:true with the same projection succeeds", func(t *testing.T) {
		res := call(t, map[string]any{
			"format": "json", "include_source": true, "fields": []string{"content"},
		})
		require.False(t, res.IsError, textBodyTools(res))
		rows := projectedRows(t, textBodyTools(res))
		require.Len(t, rows, 1)
		assert.Equal(t, codeBodyText, rows[0]["content"], "only the CONJUNCTION is refused")
	})

	t.Run("control: include_source:false with a body-free projection succeeds", func(t *testing.T) {
		res := call(t, map[string]any{
			"format": "json", "include_source": false, "fields": []string{"id"},
		})
		require.False(t, res.IsError, textBodyTools(res))
		rows := projectedRows(t, textBodyTools(res))
		require.Len(t, rows, 1)
		assert.Equal(t, []string{"id"}, rowKeys(rows[0]), "only the CONJUNCTION is refused")
	})

	t.Run("the query tool is refused at the same chokepoint", func(t *testing.T) {
		handled, res := InterceptQueryCodeSearch(opCtx(), codeSeedDeps(t, codeBodyNode()),
			queryToolParams(t, map[string]any{
				"graph": "code", "repo": "knowledge", "text": "foo", "format": "json",
				"include_source": false, "fields": []string{"content"},
			}))
		require.True(t, handled)
		require.True(t, res.IsError, "both entry points funnel through one composer: %s", textBodyTools(res))
		assert.Contains(t, textBodyTools(res), "include_source")
		assert.Contains(t, textBodyTools(res), "fields")
	})
}
