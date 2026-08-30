// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// vocabNode builds a node with EVERY projectable field populated to a DISTINCT
// non-zero value. Distinctness is the point: a fixture deriving two
// conceptually different keys from one value cannot tell a mis-wired arm from a
// correct one, and CreatedAt / UpdatedAt are exactly the pair most likely to be
// cross-wired, so they must not share a value.
func vocabNode() *knowledgev1.Node {
	return &knowledgev1.Node{
		Id:          "vocab-node-1",
		Type:        "vocab-type",
		SymbolName:  "VocabSymbol",
		FilePath:    "path/to/vocab.go",
		Language:    "vocab-language",
		StartLine:   4242,
		Content:     "vocab content body",
		Signature:   "func VocabSymbol() error",
		Summary:     "vocab summary",
		Description: "vocab description",
		Source:      "vocab source",
		Status:      "vocab status",
		Keywords:    "vocab keywords",
		TestKind:    "vocab test kind",
		Metadata:    map[string]string{"vocab_key": "vocab metadata value"},
		CreatedAt:   1600000000000000001,
		UpdatedAt:   1700000000000000002,
	}
}

// vocabResult wraps vocabNode in a hit carrying the three hit-only properties.
func vocabResult() SearchResult {
	return SearchResult{
		Node:          vocabNode(),
		Score:         0.7539,
		Graph:         "vocab-graph",
		GraphInstance: "vocab-graph-instance",
	}
}

// vocabTombstonedAt is the fixture tombstone stamp. It is DISTINCT from both
// CreatedAt and UpdatedAt on purpose: a projector wired to the wrong timestamp
// field would otherwise pass every assertion that reads it.
const vocabTombstonedAt int64 = 1800000000000000003

// vocabTombstonedNode is vocabNode with a tombstone stamp. The serving loops
// need it because tombstoned_at is the one declared key a LIVE node deliberately
// omits, so a live fixture cannot demonstrate that the key is served at all.
func vocabTombstonedNode() *knowledgev1.Node {
	n := vocabNode()
	n.TombstonedAt = vocabTombstonedAt
	return n
}

// vocabTombstonedResult is vocabResult over the tombstoned node, for the hit
// grammar's half of the same problem.
func vocabTombstonedResult() SearchResult {
	r := vocabResult()
	r.Node = vocabTombstonedNode()
	return r
}

// TestProjectionVocabulary_NodeAndHitSetsServeEveryDeclaredKey is the test that
// would have caught the original defect — a key declared as projectable that no
// projection arm serves, dropped silently — and the one that makes a future
// revert to silence go red.
//
// It has two halves and both are required: the set half alone is satisfied by a
// declaration nobody implements.
//
// It asserts key SETS, and per-key VALUES only for keys whose mapping is
// identical in both grammars. The two grammars deliberately DIVERGE on `name`'s
// value semantics — ProjectNodeJSON returns SymbolName, projectHydratedResult
// falls back to Description when SymbolName is empty (render_search.go) — and
// that divergence is preserved on purpose, because aligning them would change
// what the search tool renders as `name` for every symbol-less node. The
// omission of a `name` value assertion is therefore deliberate, not a gap.
func TestProjectionVocabulary_NodeAndHitSetsServeEveryDeclaredKey(t *testing.T) {
	// HALF ONE — set parity, as SET equality rather than a length comparison.
	// A count is satisfied by dropping one member and adding another.
	t.Run("hit set is exactly the node set plus the hit-only keys", func(t *testing.T) {
		want := make([]string, 0, len(nodeProjectionKeys)+len(hitOnlyProjectionKeys))
		want = append(want, nodeProjectionKeys...)
		want = append(want, hitOnlyProjectionKeys...)
		require.ElementsMatch(t, want, hitProjectionKeys)
	})

	n := vocabNode()
	r := vocabResult()

	// The keys Phase 1 Step 2 ADDED to ProjectNodeJSON, with the field each must
	// read. Presence alone would pass an arm that assigns the wrong field, which
	// for created_at / updated_at is the likeliest defect. The timestamps are
	// asserted as the RAW int64 nanos the fixture set — a test asserting a
	// formatted value would silently license the wrong rendering.
	nodeValues := map[string]any{
		"content":     n.Content,
		"created_at":  n.CreatedAt,
		"file_path":   n.FilePath,
		"keywords":    n.Keywords,
		"language":    n.Language,
		"line":        n.StartLine,
		"signature":   n.Signature,
		"source":      n.Source,
		"summary":     n.Summary,
		"symbol_name": n.SymbolName,
		"test_kind":   n.TestKind,
		"updated_at":  n.UpdatedAt,
	}

	// The keys Phase 1 Step 2 ADDED to projectHydratedResult.
	hitValues := map[string]any{
		"content":    n.Content,
		"created_at": n.CreatedAt,
		"updated_at": n.UpdatedAt,
	}

	// HALF TWO — every declared key is actually SERVED. One key per call, so a
	// single broken arm is named rather than hidden inside a bulk assertion.
	//
	// THE FIXTURE IS THE TOMBSTONED NODE, not the live one, and that is required
	// rather than incidental: tombstoned_at is the one declared key a LIVE node
	// deliberately omits, so asking a live fixture to serve every declared key
	// would demand exactly the sentinel the absent-vs-zero contract forbids. The
	// live node keeps its own assertion in the paired subtest below, so switching
	// the fixture here removes no coverage — it moves it.
	tn := vocabTombstonedNode()
	tr := vocabTombstonedResult()

	t.Run("ProjectNodeJSON serves every nodeProjectionKeys member", func(t *testing.T) {
		for _, key := range nodeProjectionKeys {
			out := ProjectNodeJSON(tn, []string{key})
			require.Contains(t, out, key, "ProjectNodeJSON must serve declared key %q", key)
			if want, ok := nodeValues[key]; ok {
				require.Equal(t, want, out[key], "ProjectNodeJSON key %q must read its own field", key)
			}
		}
	})

	t.Run("projectHydratedResult serves every hitProjectionKeys member", func(t *testing.T) {
		for _, key := range hitProjectionKeys {
			out := projectHydratedResult(tr, []string{key})
			require.Contains(t, out, key, "projectHydratedResult must serve declared key %q", key)
			if want, ok := hitValues[key]; ok {
				require.Equal(t, want, out[key], "projectHydratedResult key %q must read its own field", key)
			}
		}
	})

	// HALF TWO'S PAIR — the live node still serves every OTHER declared key, and
	// omits only tombstoned_at. Without this the fixture switch above would be a
	// silent weakening: a projector that served nothing but the tombstone stamp
	// correctly would still satisfy the loops, because they would never see a live
	// row again.
	t.Run("a live row serves every declared key except tombstoned_at", func(t *testing.T) {
		for _, key := range nodeProjectionKeys {
			out := ProjectNodeJSON(n, []string{key})
			if key == tombstonedAtProjectionKey {
				require.NotContains(t, out, key,
					"ProjectNodeJSON must OMIT %q for a live node — a 0 here is indistinguishable from a real stamp", key)
				continue
			}
			require.Contains(t, out, key, "ProjectNodeJSON must serve declared key %q on a live node", key)
		}
		for _, key := range hitProjectionKeys {
			out := projectHydratedResult(r, []string{key})
			if key == tombstonedAtProjectionKey {
				require.NotContains(t, out, key,
					"projectHydratedResult must OMIT %q for a live hit — a 0 here is indistinguishable from a real stamp", key)
				continue
			}
			require.Contains(t, out, key, "projectHydratedResult must serve declared key %q on a live hit", key)
		}
	})
}

// TestProjectionRefusal_UnknownKeyWithControl is the ticket's headline test.
//
// BOTH halves live in ONE test function so a single run proves both. Without
// the control half, a refuse-everything implementation satisfies the refusal
// assertion perfectly — the assertion would be measuring that the renderer
// errors, not that it errors on the RIGHT input.
func TestProjectionRefusal_UnknownKeyWithControl(t *testing.T) {
	nodes := []*knowledgev1.Node{vocabNode()}

	t.Run("unknown key is refused, naming the key and the vocabulary", func(t *testing.T) {
		out, err := renderNodesByIDsResponse(nodesResp(t, nodes, 1), "knowledge", "json", []string{"id", "zzz_no_such_field"}, false)
		require.NoError(t, err, "a caller-input refusal is a rendered error result, not a transport error")
		require.True(t, out.IsError, "an unsupported projection key must be refused")
		msg := out.Content[0].Text
		require.Contains(t, msg, "unsupported projection key")
		require.Contains(t, msg, "zzz_no_such_field", "the message must name the offending key")
		// Three accepted keys, so the message is proven to carry the LIST rather
		// than only the offending key.
		require.Contains(t, msg, "summary")
		require.Contains(t, msg, "description")
		require.Contains(t, msg, "metadata")
	})

	t.Run("control: a valid projection still returns its fields", func(t *testing.T) {
		out, err := renderNodesByIDsResponse(nodesResp(t, nodes, 1), "knowledge", "json", []string{"id", "name"}, false)
		require.NoError(t, err)
		require.False(t, out.IsError, "a declared projection key must not be refused")
		var payload struct {
			Nodes []map[string]any `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		require.Len(t, payload.Nodes, 1)
		require.Equal(t, "vocab-node-1", payload.Nodes[0]["id"])
		require.Equal(t, "VocabSymbol", payload.Nodes[0]["name"])
	})
}

// TestProjectionRefusal_HitOnlyKeyOnNodeRead pins the SECOND refusal message —
// the one telling a caller where a hit-only key IS available.
//
// The second half is what stops that message from being merely plausible: it
// drives the SAME key through the hit grammar and requires it to project,
// proving `score` is a real key one grammar over rather than a typo.
func TestProjectionRefusal_HitOnlyKeyOnNodeRead(t *testing.T) {
	fields := []string{"id", "score"}

	t.Run("a hit-only key on a node read is refused with where-to-find-it", func(t *testing.T) {
		out, err := renderNodesByIDsResponse(nodesResp(t, []*knowledgev1.Node{vocabNode()}, 1), "knowledge", "json", fields, false)
		require.NoError(t, err)
		require.True(t, out.IsError, "a hit-only key must be refused on a node read")
		msg := out.Content[0].Text
		require.Contains(t, msg, "is a search-result property")
		require.Contains(t, msg, "score", "the message must name the offending key")
	})

	t.Run("the same key still projects through the hit grammar", func(t *testing.T) {
		out := RenderForCaller("q", []SearchResult{vocabResult()}, "json", fields, "BM25-only")
		require.False(t, out.IsError, "score is a declared key on a ranked-search read")
		var payload struct {
			Results []map[string]any `json:"results"`
		}
		require.NoError(t, json.Unmarshal([]byte(out.Content[0].Text), &payload))
		require.Len(t, payload.Results, 1)
		require.Contains(t, payload.Results[0], "score")
		require.InDelta(t, 0.7539, payload.Results[0]["score"], 1e-9)
	})
}

// TestNodeProjection_TombstonedAtRequiresIncludeTombstones locks the whole
// tombstoned_at contract in one function, across BOTH projection arms.
//
// The contract has four cells — {live, tombstoned} x {node arm, hit arm} — plus
// the refusal, and all five are asserted here because each of the four cells
// alone is satisfiable by an implementation that is wrong in the others. In
// particular, a projectHydratedResult carrying NO tombstoned_at case at all
// would pass every OMIT leg, since it omits the key on every row; leg (e) is the
// one that catches it, and it lands on the arm measured to be the one that
// actually serves tombstoned rows.
func TestNodeProjection_TombstonedAtRequiresIncludeTombstones(t *testing.T) {
	live := vocabNode()
	dead := vocabTombstonedNode()

	// (a) Naming the key without the opt-in is REFUSED, and the message names the
	// key — a generic refusal leaves the caller no better off than a silent drop.
	t.Run("refused without the include_tombstones opt-in", func(t *testing.T) {
		err := ValidateNodeProjection([]string{"id", tombstonedAtProjectionKey}, false)
		require.Error(t, err, "tombstoned_at must be refused without the opt-in")
		require.Contains(t, err.Error(), tombstonedAtProjectionKey,
			"the refusal must NAME the offending key")
		require.Contains(t, err.Error(), "include_tombstones",
			"the refusal must name the flag that serves the key")

		// Control, same call shape: the opt-in makes the SAME projection valid, so
		// the error above is about the flag rather than about the key list.
		require.NoError(t, ValidateNodeProjection([]string{"id", tombstonedAtProjectionKey}, true))
		// Second control: an unrelated key is unaffected by the flag either way.
		require.NoError(t, ValidateNodeProjection([]string{"id", "updated_at"}, false))
	})

	// (b) A TOMBSTONED node's row carries the RAW int64 nanos the fixture set.
	t.Run("tombstoned node carries raw nanos through ProjectNodeJSON", func(t *testing.T) {
		out := ProjectNodeJSON(dead, []string{"id", tombstonedAtProjectionKey})
		require.Contains(t, out, tombstonedAtProjectionKey)
		require.Equal(t, vocabTombstonedAt, out[tombstonedAtProjectionKey],
			"tombstoned_at must project the raw int64 nanos, not a formatted value")
		require.NotEqual(t, dead.UpdatedAt, out[tombstonedAtProjectionKey],
			"tombstoned_at must read TombstonedAt, not a neighboring timestamp")
	})

	// (c) A LIVE node's row OMITS the key entirely through the node arm.
	t.Run("live node omits the key through ProjectNodeJSON", func(t *testing.T) {
		out := ProjectNodeJSON(live, []string{"id", tombstonedAtProjectionKey})
		require.NotContains(t, out, tombstonedAtProjectionKey,
			"a live node must OMIT tombstoned_at — 0 is indistinguishable at the wire from a real stamp")
		// Known-positive in the SAME call: the projection ran and served its other
		// key, so the omission above is the contract rather than an empty result.
		require.Equal(t, live.Id, out["id"])
	})

	// (d) A LIVE hit row OMITS the key through the hit arm.
	t.Run("live hit omits the key through projectHydratedResult", func(t *testing.T) {
		out := projectHydratedResult(vocabResult(), []string{"id", tombstonedAtProjectionKey})
		require.NotContains(t, out, tombstonedAtProjectionKey,
			"a live hit must OMIT tombstoned_at on the hit arm too")
		require.Equal(t, live.Id, out["id"])
	})

	// (e) A TOMBSTONED hit row CARRIES the raw nanos through the hit arm. This is
	// the leg that fails an implementation with no tombstoned_at case at all.
	t.Run("tombstoned hit carries raw nanos through projectHydratedResult", func(t *testing.T) {
		out := projectHydratedResult(vocabTombstonedResult(), []string{"id", tombstonedAtProjectionKey})
		require.Contains(t, out, tombstonedAtProjectionKey,
			"the hit arm must SERVE tombstoned_at for a tombstoned row")
		require.Equal(t, vocabTombstonedAt, out[tombstonedAtProjectionKey],
			"tombstoned_at must project the raw int64 nanos on the hit arm too")
	})
}
