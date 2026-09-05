// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// refusedByIDSubstring is the locked phrase the refusal message carries. The
// read-side dispatch parity grid keys on it, so the two cannot drift apart.
const refusedByIDSubstring = "is not applied by a by-id read"

// TestPrecheckQuery_FilterAlongsideIDSelectorRefused pins both halves of the
// by-id filter refusal: a filter or search term supplied alongside id/ids is
// refused before any Execute RPC, and the shapes that legitimately work are not
// caught by it.
//
// The mode-less compile precedence is ids → id → text → types → type, so when an
// id-selector is present the plan is built by the ids or id arm and returns —
// every filter arm below is unreachable and the filter is silently inert. The
// caller would otherwise get a successful non-empty answer with no signal.
//
// Driven END-TO-END through Dispatch rather than calling the precheck in
// isolation, so a future regression where the seam stops being invoked is caught
// here rather than passing against a helper nothing calls.
//
// The four still-works rows are the anti-false-rejection half: the refusal has
// to be narrow enough to leave a plain lookup, a plain browse, and the
// status-bearing id shape alone. They are green before and after this change.
func TestPrecheckQuery_FilterAlongsideIDSelectorRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
		// param is the field the refusal must name; empty means this row is
		// expected to pass through untouched.
		param string
	}{
		{"id-plus-type", `{"id":"n1","type":"finding"}`, "type"},
		{"id-plus-text", `{"id":"n1","text":"auth"}`, "text"},
		{"ids-plus-types", `{"ids":["n1"],"types":["finding"]}`, "types"},
		{"id-plus-meta", `{"id":"n1","meta":{"k":"v"}}`, "meta"},
		{"id-alone", `{"id":"n1"}`, ""},
		{"ids-alone", `{"ids":["n1"]}`, ""},
		{"type-alone", `{"type":"finding"}`, ""},
		// This shows only that the precheck does not refuse status — true by
		// construction, since status is not in the refused list. It CANNOT observe
		// the upstream recall claim that justifies the exclusion, because there is
		// no intercept chain at this layer; that routing is observable only in the
		// chain-driven parity grid.
		{"id-plus-status", `{"id":"n1","status":"open"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &dispatchCounters{}
			execFn := d.exec(&knowledgev1.ExecuteResponse{}, nil)
			if tc.param != "" {
				execFn = d.exec(nil, errors.New("exec must not run — the precheck refuses this shape"))
			}
			out, err := Dispatch(context.Background(), execFn, nil, "query", json.RawMessage(tc.args))
			require.NoError(t, err, "the refusal is rendered as an error result, not returned")
			body := out.Content[0].Text

			if tc.param == "" {
				assert.NotContains(t, body, refusedByIDSubstring,
					"%s must NOT be caught by the by-id filter refusal", tc.name)
				assert.Equal(t, 1, d.execCalls, "%s still reaches Execute", tc.name)
				return
			}
			assert.True(t, out.IsError, "%s is a validation failure", tc.name)
			assert.Contains(t, body, refusedByIDSubstring, "the locked refusal phrase must be present")
			assert.Contains(t, body, tc.param, "the refusal must NAME the offending param")
			assert.Equal(t, 0, d.execCalls, "%s must be refused BEFORE any Execute RPC", tc.name)
		})
	}
}
