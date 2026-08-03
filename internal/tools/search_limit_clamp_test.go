// SPDX-License-Identifier: Apache-2.0

package tools

// search_limit_clamp_test.go covers the search-tool boundary clamp: that the
// declared `limit` maximum binds on every serving path, that the caller is told
// when it engages, and — the part a single composed test cannot see — that the
// clamp runs BEFORE the rerank rewrite widens the same wire key to the
// candidate-pool size.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// clampNoticeFragment is the load-bearing slice of the disclosure copy. Matching
// a fragment rather than the whole line keeps the assertion honest without
// duplicating product copy into the test.
const clampNoticeFragment = "maximum of 50 engaged"

func TestClampSearchCallerLimit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		payload     string
		wantLimit   string // the limit value expected in the returned args, "" = unchanged
		wantClamped bool
	}{
		{"absent limit passes through", `{"query":"x"}`, "", false},
		{"zero passes through", `{"query":"x","limit":0}`, "", false},
		{"negative passes through", `{"query":"x","limit":-5}`, "", false},
		{"under the ceiling passes through", `{"query":"x","limit":10}`, "", false},
		{"over the ceiling clamps", `{"query":"x","limit":500}`, "50", true},
		{"the ceiling itself passes through", `{"query":"x","limit":50}`, "", false},
		{"malformed json passes through", `{not json`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, clamped := clampSearchCallerLimit([]byte(tc.payload))
			assert.Equal(t, tc.wantClamped, clamped)
			if tc.wantClamped {
				var obj map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(got, &obj))
				assert.JSONEq(t, tc.wantLimit, string(obj["limit"]))
				return
			}
			assert.Equal(t, tc.payload, string(got), "a non-clamping call must return the args byte-identical")
		})
	}
}

// TestInterceptSearch_KeylessLimitClampedAndDisclosed is the keyless serving
// path: no rerank rewrite runs at all, so this proves the clamp does not depend
// on the rerank pipeline being active.
func TestInterceptSearch_KeylessLimitClampedAndDisclosed(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")

	t.Run("text_render", func(t *testing.T) {
		deps, mgr, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "knowledge", "query": "x", "limit": 500,
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "%v", engine.FirstTextContent(out))
		assert.Equal(t, rerankCallerLimitCeiling, mgr.lastK)
		assert.True(t, blocksCarryClampNotice(out.Content), "the clamp must be disclosed")
	})

	t.Run("json_render_still_parses", func(t *testing.T) {
		deps, mgr, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)
		handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
			"graph": "knowledge", "query": "x", "limit": 500, "format": "json",
		}))
		require.True(t, handled)
		require.False(t, out.IsError, "%v", engine.FirstTextContent(out))
		assert.Equal(t, rerankCallerLimitCeiling, mgr.lastK)
		assert.True(t, blocksCarryClampNotice(out.Content), "the clamp must be disclosed")
		// The notice rides as a SEPARATE block, so the json body stays parseable.
		var probe map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(engine.FirstTextContent(out)), &probe),
			"the first block must remain valid json — a concatenated notice would corrupt it")
	})
}

// TestInterceptSearch_RegisteredGraphLimitClampedAndDisclosed proves the same
// boundary covers the custom-graph arm, which the rerank pipeline never touches.
func TestInterceptSearch_RegisteredGraphLimitClampedAndDisclosed(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	deps, mgr := newRegisteredParityDeps(t, registeredParityNodes(), registeredParityHits())

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "hellograph", "name": "demo", "query": "world", "limit": 500,
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "%v", engine.FirstTextContent(out))
	assert.Equal(t, rerankCallerLimitCeiling, mgr.lastK)
	assert.True(t, blocksCarryClampNotice(out.Content))
}

// TestInterceptSearch_UnderTheMaximumIsUntouched is the anti-blanket control:
// without it, a clamp hardwired to 50 with an always-on notice would satisfy
// every other test in this file.
func TestInterceptSearch_UnderTheMaximumIsUntouched(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "")
	deps, mgr, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)

	handled, out := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "query": "x", "limit": 10,
	}))
	require.True(t, handled)
	require.False(t, out.IsError, "%v", engine.FirstTextContent(out))
	assert.Equal(t, 10, mgr.lastK, "a limit under the ceiling reaches the engine untouched")
	assert.False(t, blocksCarryClampNotice(out.Content),
		"disclosing a clamp that did not engage is its own kind of lie")
}

// TestClampThenRewrite_CallerLimitAndPoolStaySeparate composes the two writers
// of the `limit` key in the required order and asserts they carry DIFFERENT
// facts: the caller cap lands in savedState, the candidate-pool size lands on
// the wire.
//
// SCOPE, stated honestly: this fixes the call order in its own body, so it
// cannot detect a clamp misplaced inside InterceptSearch. That is what the
// sibling ordering test exists for.
func TestClampThenRewrite_CallerLimitAndPoolStaySeparate(t *testing.T) {
	clampedArgs, clamped := clampSearchCallerLimit([]byte(`{"query":"x","limit":500}`))
	require.True(t, clamped)

	out, saved, hasRewrite, err := rewriteSearchArgs(clampedArgs, true)
	require.NoError(t, err)
	require.True(t, hasRewrite)

	assert.Equal(t, rerankCallerLimitCeiling, saved.originalLimit,
		"the CALLER cap is what the post-rerank trim cuts to")

	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(out, &obj))
	var wireLimit int
	require.NoError(t, json.Unmarshal(obj["limit"], &wireLimit))
	assert.Equal(t, widePoolSize, wireLimit,
		"the WIRE limit is the candidate pool — collapsing the two would gut the rerank input")
}

// TestInterceptSearch_ClampPrecedesPoolWidening is the ONLY test that sees a
// clamp misplaced AFTER the rewrite: in that arrangement the clamped 50 would
// overwrite the widened pool and lastK would read 50 instead of the pool size.
//
// It deliberately sets a FAKE key — the one exception to this package's
// keyless discipline — because the widening only happens on the keyed path. The
// Voyage call itself silent-degrades on the fake credential, which does not
// affect the observable: lastK is recorded before any rerank runs.
func TestInterceptSearch_ClampPrecedesPoolWidening(t *testing.T) {
	t.Setenv("VOYAGE_API_KEY", "test-key")
	deps, mgr, _ := newModeHonorDeps(t, modeHonorNodes(), modeHonorHits(), true)

	handled, _ := InterceptSearch(opCtx(), deps, searchParams(t, map[string]any{
		"graph": "knowledge", "query": "x", "limit": 500,
	}))
	require.True(t, handled)
	assert.Equal(t, widePoolSize, mgr.lastK,
		"the rerank pool must survive the clamp; reading 50 here means the clamp ran after the widening")
}

// blocksCarryClampNotice reports whether any content block carries the
// declared-maximum disclosure. It reads EVERY block on purpose: the notice
// ships as a sibling of the render rather than inside it, so a helper that
// looked only at the first block could never see it.
func blocksCarryClampNotice(blocks []kgtools.ContentBlock) bool {
	for _, b := range blocks {
		if strings.Contains(b.Text, clampNoticeFragment) {
			return true
		}
	}
	return false
}
