// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWidePoolInvariants pins the RELATIONSHIPS between the rerank pool
// constants rather than their literal values. A test asserting the literals
// would have to be edited on every retune and would catch nothing the
// constant-value gate does not already catch; these two invariants are what
// must survive any retune.
//
//	(a) top_k never exceeds the pool. Goes red on the half-applied edit this
//	    change is most at risk of — pool lowered, top_k left behind — which
//	    would put dead weight in every rerank request body.
//	(b) the pool keeps at least 2x headroom over the largest limit a caller
//	    may declare (the tool schema's max of 50), so the pool can always
//	    satisfy the biggest declared return. Goes red if a future retune
//	    drops the pool below what the contract promises callers.
func TestWidePoolInvariants(t *testing.T) {
	assert.LessOrEqual(t, widePoolTopK, widePoolSize,
		"top_k above the document count is dead weight in the request body")

	const declaredCallerMax = 50
	assert.GreaterOrEqual(t, widePoolSize, 2*declaredCallerMax,
		"pool must keep 2x headroom over the declared caller limit maximum")
}

// TestClampCallerLimit covers the clamp helper's whole range, including the two
// cases a helper copied from a substitute-the-ceiling clamp would get wrong: the
// ceiling itself must pass through UNFLAGGED, and an absent limit (0) must pass
// through UNTOUCHED rather than being widened to 50.
func TestClampCallerLimit(t *testing.T) {
	cases := []struct {
		name        string
		requested   int
		wantLimit   int
		wantClamped bool
	}{
		{"far above the ceiling clamps", 200, 50, true},
		{"one above the ceiling clamps", 51, 50, true},
		{"the ceiling itself passes through unflagged", 50, 50, false},
		{"below the ceiling passes through", 10, 10, false},
		{"absent must not widen", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotLimit, gotClamped := clampCallerLimit(tc.requested)
			assert.Equal(t, tc.wantLimit, gotLimit)
			assert.Equal(t, tc.wantClamped, gotClamped)
		})
	}
}

// TestCaptureSavedState_RecordsCallerLimitVerbatim pins that this function is
// NOT a second clamp authority: the args reach it already clamped by the
// search-tool boundary, so it records what it is given. A re-clamp here would
// be a second authority over one value, which is the shape that made the
// caller cap and the rerank pool inseparable in the first place.
//
// The zero case is the POSITION PIN and survives the move. The lim > 0 guard
// must stay: an explicit "limit": 0 setting originalLimit to 0 would make
// applyClientRerank's trim (gated on originalLimit > 0) skip entirely, handing
// the caller the whole candidate pool.
func TestCaptureSavedState_RecordsCallerLimitVerbatim(t *testing.T) {
	capture := func(t *testing.T, payload string) savedState {
		t.Helper()
		var argMap map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(payload), &argMap))
		return captureSavedState(argMap, true)
	}

	t.Run("records a pre-clamped limit verbatim", func(t *testing.T) {
		saved := capture(t, `{"limit":50}`)
		assert.Equal(t, 50, saved.originalLimit)
	})

	t.Run("does not re-clamp an over-ceiling value", func(t *testing.T) {
		// Reaching this function with 200 means the boundary did not clamp, which
		// is a boundary bug — but silently re-clamping here would HIDE it behind a
		// plausible-looking 50 and leave two authorities racing over one key.
		saved := capture(t, `{"limit":200}`)
		assert.Equal(t, 200, saved.originalLimit,
			"the boundary is the single clamp authority; this function records, it does not decide")
	})

	t.Run("under the maximum is untouched", func(t *testing.T) {
		saved := capture(t, `{"limit":10}`)
		assert.Equal(t, 10, saved.originalLimit)
	})

	t.Run("absent limit keeps the struct default", func(t *testing.T) {
		saved := capture(t, `{"query":"q"}`)
		assert.Equal(t, 10, saved.originalLimit)
	})

	t.Run("explicit zero limit keeps the struct default", func(t *testing.T) {
		saved := capture(t, `{"limit":0}`)
		assert.Equal(t, 10, saved.originalLimit,
			"a zero limit must not zero originalLimit — that would skip the post-rerank trim entirely")
	})
}
