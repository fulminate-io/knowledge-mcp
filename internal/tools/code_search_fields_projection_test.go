// SPDX-License-Identifier: Apache-2.0

// code_search_fields_projection_test.go — the code-search arm's `fields` json
// projection and the two input gates that ride with it.
//
// EVERY TEST HERE DRIVES A REAL ENTRY POINT with a raw host payload
// (interceptSearchCode for the search tool, InterceptQueryCodeSearch for the
// query tool) rather than a sub-composer, because the defect being closed was
// precisely that the decode-to-render path dropped a parameter both tool schemas
// advertise: a sub-composer test hands the projection in by hand and so cannot
// observe the drop.
//
// THE ROWS ARE DECODED AS MAPS, NEVER AS engine.SearchJSONResponse. That struct
// absorbs every unrequested key into a typed field, so a key-set assertion made
// through it passes vacuously against an unprojected envelope — which is the
// exact output this work removes.

package tools

import (
	"encoding/json"
	"maps"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// projectedRows decodes a rendered json body into the projected envelope, whose
// rows stay map[string]any so a key the arm emitted but the caller never asked
// for is VISIBLE to the assertion. Mirrors renderJSONProjected's own envelope
// shape (query/total/results/truncated).
func projectedRows(t *testing.T, body string) []map[string]any {
	t.Helper()
	var env struct {
		Query     string           `json:"query"`
		Total     int              `json:"total"`
		Results   []map[string]any `json:"results"`
		Truncated bool             `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &env), "json branch must parse to the projected envelope; body=%s", body)
	return env.Results
}

// rowKeys returns a row's key set sorted, so the assertion can be an EQUALITY
// against the requested projection rather than a containment check — a
// containment check is what an unprojected envelope also satisfies.
func rowKeys(row map[string]any) []string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// codeSeedDeps wires the single-repo code seam exactly as the JSON contract test
// at search_test.go:229-237 does: the intercept harness serves the ids[] hydrate
// for the canned node, and the fake segment searcher returns one hit for it.
func codeSeedDeps(t *testing.T, node *knowledgev1.Node) *interceptDeps {
	t.Helper()
	var execHits atomic.Int64
	gc := newInterceptHarness(t, &execHits, cannedNodesResp(node))
	mgr := &fakeSegmentSearcher{hits: []searchengine.Hit{{ID: node.GetId(), Score: 0.9}}}
	return &interceptDeps{gc: gc, segMgr: mgr}
}

// codeSeedNode is the minimal symbol node the key-set assertions are measured
// against. It populates NO Language, Summary, Signature, Keywords, Description
// or Content, so the unprojected envelope's omitempty tags drop those keys and
// the unprojected key set is a stable nine.
func codeSeedNode() *knowledgev1.Node {
	return &knowledgev1.Node{
		Id: "f.go:Foo", SymbolName: "Foo", Type: "function", FilePath: "f.go", StartLine: 1,
	}
}

// queryToolParams builds a query-tool CallToolParams from an argument map, the
// query-tool twin of searchParams.
func queryToolParams(t *testing.T, args map[string]any) kgtools.CallToolParams {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return kgtools.CallToolParams{Name: "query", Arguments: raw}
}

// TestCodeSearchFieldsProjection_SingleRepo is R1a and R1c, and it is the R4
// SEAM TEST for the search tool: the raw host payload carrying `fields` reaches
// the code composer through the real intercept and the rendered row carries
// ONLY the projected keys.
func TestCodeSearchFieldsProjection_SingleRepo(t *testing.T) {
	t.Run("the row's key set equals the projection", func(t *testing.T) {
		handled, res := interceptSearchCode(opCtx(), codeSeedDeps(t, codeSeedNode()), nil,
			json.RawMessage(`{"graph":"code","query":"foo","repo":"knowledge","format":"json","fields":["id","file_path","score"]}`))
		require.True(t, handled, "graph=code must be claimed client-side")
		require.False(t, res.IsError, textBodyTools(res))
		rows := projectedRows(t, textBodyTools(res))
		require.Len(t, rows, 1)
		assert.Equal(t, []string{"file_path", "id", "score"}, rowKeys(rows[0]),
			"a projected code row carries the requested keys and nothing else")
		assert.Equal(t, "f.go:Foo", rows[0]["id"])
		assert.Equal(t, "f.go", rows[0]["file_path"])
	})

	t.Run("the hit properties project alongside the node keys", func(t *testing.T) {
		handled, res := interceptSearchCode(opCtx(), codeSeedDeps(t, codeSeedNode()), nil,
			json.RawMessage(`{"graph":"code","query":"foo","repo":"knowledge","format":"json","fields":["score","graph","graph_instance"]}`))
		require.True(t, handled)
		require.False(t, res.IsError, textBodyTools(res))
		rows := projectedRows(t, textBodyTools(res))
		require.Len(t, rows, 1)
		assert.Equal(t, []string{"graph", "graph_instance", "score"}, rowKeys(rows[0]))
		assert.Equal(t, "code", rows[0]["graph"], "the code arm stamps graph=code")
		assert.Equal(t, "knowledge", rows[0]["graph_instance"], "single-repo stamps the request repo as the instance")
		assert.InDelta(t, 0.9, rows[0]["score"], 0.0001)
	})
}

// TestCodeSearchFieldsProjection_MultiRepo is R1b: the SAME payload with
// repos:[...] dispatches to the multi-repo composer (composeCodeSearch:215
// branches on len(a.Repos)>0) through the real intercept, so this covers the
// second render site rather than restating the first.
func TestCodeSearchFieldsProjection_MultiRepo(t *testing.T) {
	// The empty temp manifest pins both repos to the no-entry branch state, so
	// the fan-out stays on the single-pool path regardless of what this developer
	// has collected.
	withTestManifest(t)
	gc := newFanOutHarness(t, []string{"repoA", "repoB"},
		&knowledgev1.Node{Id: "a.go:A", SymbolName: "A", Type: "function", FilePath: "a.go", StartLine: 1},
		&knowledgev1.Node{Id: "b.go:B", SymbolName: "B", Type: "function", FilePath: "b.go", StartLine: 1},
	)
	mgr := newFanOutSegmentSearcher(map[string][]searchengine.Hit{
		"repoA": {{ID: "a.go:A", Score: 0.90}},
		"repoB": {{ID: "b.go:B", Score: 0.70}},
	})
	deps := &interceptDeps{gc: gc, segMgr: mgr}

	handled, res := interceptSearchCode(opCtx(), deps, nil,
		json.RawMessage(`{"graph":"code","query":"x","repos":["repoA","repoB"],"format":"json","fields":["id","graph_instance"]}`))
	require.True(t, handled)
	require.False(t, res.IsError, textBodyTools(res))
	rows := projectedRows(t, textBodyTools(res))
	require.Len(t, rows, 2, "both repos' hits flatten into the projected envelope")

	byID := map[string]map[string]any{}
	for _, row := range rows {
		assert.Equal(t, []string{"graph_instance", "id"}, rowKeys(row),
			"every cross-repo row carries the requested keys and nothing else")
		id, _ := row["id"].(string)
		byID[id] = row
	}
	// The per-result instance VARIES across repos, so asserting both is what
	// proves the projection rode the merge rather than one repo's leg.
	assert.Equal(t, "repoA", byID["a.go:A"]["graph_instance"])
	assert.Equal(t, "repoB", byID["b.go:B"]["graph_instance"])
}

// TestCodeSearchFieldsProjection_UnsupportedKeyRefused is R1d: an unsupported
// projection key is REFUSED naming the key and the accepted vocabulary, on both
// entry points and under BOTH render formats — the yardstick R1 names is the
// knowledge arm, which validates ahead of its own format switch
// (engine/render_search.go:109-113), so the code arm refuses under format:"text"
// too rather than dropping a bad key on the render path that ignores it.
//
// THE PAYLOAD IS ASSEMBLED, NOT WRITTEN AS A LITERAL. The CI projection-key
// census (scripts/ful1528_projection_key_census.py) scans every tracked .go file
// including this one, and its first marker matches a `"fields":[` literal and
// reads the next quoted token — so a bad key written inline here, even built by
// concatenation, is reported as an out-of-vocabulary projection site and turns
// CI red. Routing the list through a Go variable keeps the fixture out of the
// scanned text, which is the same move the census's own positive control makes
// at scripts/ful1528_projection_key_census.py:137-138.
func TestCodeSearchFieldsProjection_UnsupportedKeyRefused(t *testing.T) {
	badKey := "zzz_no" + "_such_field"
	projection := []string{"id", badKey}

	payload := func(t *testing.T, extra map[string]any) json.RawMessage {
		t.Helper()
		args := map[string]any{
			"graph": "code", "repo": "knowledge", "fields": projection,
		}
		maps.Copy(args, extra)
		raw, err := json.Marshal(args)
		require.NoError(t, err)
		return raw
	}

	for _, tc := range []struct {
		name   string
		format string
	}{
		{"json render path", "json"},
		{"text render path", ""},
	} {
		t.Run("search tool — "+tc.name, func(t *testing.T) {
			extra := map[string]any{"query": "foo"}
			if tc.format != "" {
				extra["format"] = tc.format
			}
			handled, res := interceptSearchCode(opCtx(), codeSeedDeps(t, codeSeedNode()), nil, payload(t, extra))
			require.True(t, handled, "a refused projection is still a claimed call, not a fall-through")
			require.True(t, res.IsError, "an unsupported projection key must be refused: %s", textBodyTools(res))
			body := textBodyTools(res)
			assert.Contains(t, body, badKey, "the refusal names the offending key")
			assert.Contains(t, body, "symbol_name", "the refusal names the accepted vocabulary")
		})
	}

	t.Run("query tool", func(t *testing.T) {
		extra := map[string]any{"text": "foo", "format": "json"}
		handled, res := InterceptQueryCodeSearch(opCtx(), codeSeedDeps(t, codeSeedNode()),
			kgtools.CallToolParams{Name: "query", Arguments: payload(t, extra)})
		require.True(t, handled)
		require.True(t, res.IsError, "an unsupported projection key must be refused: %s", textBodyTools(res))
		assert.Contains(t, textBodyTools(res), badKey)
	})
}

// TestInterceptQueryCodeSearch_FieldsProjection is R2 and the R4 seam test for
// the QUERY tool: the same projection through the query entry point returns the
// same projected rows, because both entry points funnel through one composer.
// It also pins that the arm's own accountQueryParams gate (:86) still admits the
// call — a registry cell that classified `fields` as rejected would fail here.
func TestInterceptQueryCodeSearch_FieldsProjection(t *testing.T) {
	handled, res := InterceptQueryCodeSearch(opCtx(), codeSeedDeps(t, codeSeedNode()),
		queryToolParams(t, map[string]any{
			"graph": "code", "repo": "knowledge", "text": "foo", "format": "json",
			"fields": []string{"id", "symbol_name", "score"},
		}))
	require.True(t, handled, "query(graph:code) with text is the search shape")
	require.False(t, res.IsError, textBodyTools(res))
	rows := projectedRows(t, textBodyTools(res))
	require.Len(t, rows, 1)
	assert.Equal(t, []string{"id", "score", "symbol_name"}, rowKeys(rows[0]),
		"the query tool's code arm projects exactly like the search tool's")
	assert.Equal(t, "Foo", rows[0]["symbol_name"])
}

// TestCodeSearchJSONIdentity_UnprojectedAndText is R3: the unprojected json
// envelope and the default text body are unchanged by this work. There is no
// golden file pinning either output (git ls-files over both testdata dirs finds
// none), so the guard is these assertions plus the pre-existing JSON contract
// test, which stays green UNEDITED.
func TestCodeSearchJSONIdentity_UnprojectedAndText(t *testing.T) {
	jsonBody := func(t *testing.T, raw string) string {
		t.Helper()
		handled, res := interceptSearchCode(opCtx(), codeSeedDeps(t, codeSeedNode()), nil, json.RawMessage(raw))
		require.True(t, handled)
		require.False(t, res.IsError, textBodyTools(res))
		return textBodyTools(res)
	}

	t.Run("no projection keeps the full envelope", func(t *testing.T) {
		body := jsonBody(t, `{"graph":"code","query":"foo","repo":"knowledge","format":"json"}`)
		rows := projectedRows(t, body)
		require.Len(t, rows, 1)
		// Nine keys, measured against this seed: `language`, `summary`,
		// `signature`, `keywords`, `description` and `content` are dropped by
		// their omitempty tags because the seed populates none of them.
		assert.Equal(t,
			[]string{"file_path", "graph", "graph_instance", "id", "line", "name", "score", "symbol_name", "type"},
			rowKeys(rows[0]), "the unprojected envelope is untouched by this work")
		assert.Contains(t, body, `"truncated":false`, "the envelope still carries the truncation verdict")
		// The staleness footer is a TEXT-path knob: the json branch returns ahead
		// of appendStalenessFooter, so the body ends at the envelope's closing
		// brace. Pinned rather than assumed — a footer appended here would also
		// break the decode above, but this says which property is load-bearing.
		assert.True(t, strings.HasSuffix(body, "}"), "no staleness footer is appended to a json body: %q", body)
	})

	t.Run("group_by_file is a text-path knob the json branch ignores", func(t *testing.T) {
		with := jsonBody(t, `{"graph":"code","query":"foo","repo":"knowledge","format":"json","group_by_file":true}`)
		without := jsonBody(t, `{"graph":"code","query":"foo","repo":"knowledge","format":"json"}`)
		assert.Equal(t, without, with, "the json branch returns ahead of the group_by_file render")
	})

	t.Run("the default call stays on the byte-for-byte text path", func(t *testing.T) {
		handled, res := interceptSearchCode(opCtx(), codeSeedDeps(t, codeSeedNode()), nil,
			json.RawMessage(`{"graph":"code","query":"foo","repo":"knowledge"}`))
		require.True(t, handled)
		require.False(t, res.IsError, textBodyTools(res))
		body := textBodyTools(res)
		assert.Contains(t, body, "[knowledge]")
		assert.Contains(t, body, `Found 1 results for "foo" (mode: text):`)
		assert.Contains(t, body, "Foo (function)")
	})
}

// TestCodeSearchFieldsInertOnTextPath pins WHICH WAY the text arm goes for a
// VALID projection: it is a no-op, matching the landed practice arms, which
// thread `fields` on the json branch alone. Asserted as a byte comparison
// against the same call without the projection rather than assumed.
func TestCodeSearchFieldsInertOnTextPath(t *testing.T) {
	body := func(t *testing.T, raw string) string {
		t.Helper()
		handled, res := interceptSearchCode(opCtx(), codeSeedDeps(t, codeSeedNode()), nil, json.RawMessage(raw))
		require.True(t, handled)
		require.False(t, res.IsError, textBodyTools(res))
		return textBodyTools(res)
	}
	with := body(t, `{"graph":"code","query":"foo","repo":"knowledge","fields":["id"]}`)
	without := body(t, `{"graph":"code","query":"foo","repo":"knowledge"}`)
	assert.Equal(t, without, with, "a valid projection under the text format changes nothing")
}

// TestArmCodeSearchDeclaresFieldsConsumed is R6: the query registry's
// code-search cell reads `fields` as CONSUMED rather than deliberately-ignored.
// The shared justification the cell used to carry — "the fields projection
// applies to the json render path this arm does not take" — was untrue for this
// arm, whose composer takes exactly that path at intercept_query_code_search.go.
func TestArmCodeSearchDeclaresFieldsConsumed(t *testing.T) {
	class, declared := queryParamClass(armCodeSearch, "fields")
	require.True(t, declared, "fields must be declared on the code-search arm")
	assert.Equal(t, classConsumed, class,
		"the code-search arm reads the projection, so its registry cell must say consumed")

	// CONTROL, same run: the shared helper the cell used to call is untouched and
	// still classifies fields as ignored for the 26 arms that really do drop it.
	otherClass, otherDeclared := queryParamClass(armCodeStats, "fields")
	require.True(t, otherDeclared)
	assert.Equal(t, classDeliberatelyIgnored, otherClass,
		"an arm that takes no projection keeps the ignored classification")
}
