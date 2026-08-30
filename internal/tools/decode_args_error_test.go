// SPDX-License-Identifier: Apache-2.0

package tools

// decode_args_error_test.go gates the decode-error translator: the message a
// caller sees when a param arrives in the wrong JSON shape.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// TestThoughtsArgs_ScalarForStringSlice_ErrorNamesArrayForm (FAILS-WHEN-ABSENT)
// drives the two MEASURED shapes through their real intercepts and asserts the
// translated message, plus the both-directions leg.
//
// LEG 1's NEGATIVE CLAUSE IS THE DISCRIMINATOR. Asserting the internal Go struct
// name is ABSENT is what separates a real translation from a message that merely
// prepends friendlier text to the same leaked error — the observed defect was
// "cannot unmarshal string into Go struct field thinkArgs.links of type []string",
// and a prefix-only change keeps every word of it.
func TestThoughtsArgs_ScalarForStringSlice_ErrorNamesArrayForm(t *testing.T) {
	drive := func(t *testing.T, payload string) kgtools.ToolResult {
		t.Helper()
		handled, res := InterceptThoughts(opCtx(), interceptTestDeps{gc: &fakeGraphCaller{}},
			kgtools.CallToolParams{Name: "thoughts", Arguments: json.RawMessage(payload)})
		require.True(t, handled, "the thoughts tool is claimed client-side")
		return res
	}

	t.Run("think links sent as a bare string", func(t *testing.T) {
		res := drive(t, `{"operation":"think","content":"c","summary":"s","links":"node-abc"}`)
		require.True(t, res.IsError, "a scalar where an array is declared is BAD INPUT and errors")
		body := toolResultText(res)

		assert.Contains(t, body, "links", "the refusal names the WIRE param")
		assert.Contains(t, body, `"node-abc"`, "and quotes the value the caller actually sent")
		assert.Contains(t, body, "array of strings", "and states the accepted form")
		assert.Contains(t, body, "[", "including the bracket shape to use")
		assert.NotContains(t, body, "thinkArgs",
			"the internal Go struct name must be GONE — its presence means the raw error is still leaking")
		assert.NotContains(t, body, "cannot unmarshal", "and so must the raw decoder phrasing")
	})

	t.Run("charge evidence sent as a bare string", func(t *testing.T) {
		res := drive(t, `{"operation":"charge","thought":"t-1","polarity":"positive","weight":5,`+
			`"reasoning":"r","evidence":"node-xyz"}`)
		require.True(t, res.IsError)
		body := toolResultText(res)

		assert.Contains(t, body, "evidence", "the refusal names the WIRE param")
		assert.Contains(t, body, `"node-xyz"`, "and quotes the value sent")
		assert.Contains(t, body, "array of strings")
		assert.NotContains(t, body, "chargeArgs", "the internal Go struct name must be gone")
	})

	t.Run("no coercion: the value is refused, never promoted to a singleton", func(t *testing.T) {
		// Three in-tree UnmarshalJSON implementations DO wrap a bare string into a
		// one-element slice. That shape is deliberately not copied: a param
		// silently promoted is one the caller never learns to send correctly.
		res := drive(t, `{"operation":"think","content":"c","summary":"s","links":"node-abc"}`)
		assert.True(t, res.IsError, "the call is REFUSED, not served with a coerced singleton")
	})

	t.Run("the thoughts DISPATCH decode is translated too", func(t *testing.T) {
		// A 22ND DECODE SITE the plan's census pattern could not see: it renders
		// through fmt.Sprintf("...%v", err) rather than the `+ err.Error()`
		// concatenation the census greps for, so the sweep driven by that census
		// did not reach it. It is the site a type mismatch on the four DISPATCH
		// fields (operation / mode / all_types / format) hits — the per-operation
		// decodes own everything else, which is why the two MEASURED defects
		// (thinkArgs.links, chargeArgs.evidence) were already fixed without it.
		//
		// Swept anyway on step 4.2's own stated rule: the defect is the raw decode
		// error, not the fields, and leaving one site guarantees the next
		// measurement finds it.
		res := drive(t, `{"operation":123}`)
		require.True(t, res.IsError)
		body := toolResultText(res)
		assert.Contains(t, body, "operation", "the refusal names the WIRE param")
		assert.Contains(t, body, "123", "and quotes the value sent")
		assert.NotContains(t, body, "thoughtsArgs", "the internal Go struct name must be gone")
		assert.NotContains(t, body, "cannot unmarshal", "and so must the raw decoder phrasing")
	})

	t.Run("BOTH DIRECTIONS: a correctly-formed array is accepted", func(t *testing.T) {
		// Without this leg every assertion above is satisfiable by a translator
		// that refuses every links value.
		res := drive(t, `{"operation":"think","content":"c","summary":"s","links":["node-abc"]}`)
		assert.NotContains(t, toolResultText(res), "array of strings",
			"a well-formed array must not be refused by the decode translator; got: %s", toolResultText(res))
		assert.NotContains(t, toolResultText(res), "invalid arguments",
			"the decode succeeded, so no decode refusal may be rendered")
	})
}

// TestDecodeArgsError_NonTypeErrorsPassThrough asserts the translator's scope
// boundary: it translates a TYPE mismatch and nothing else. A malformed body is
// already caller-facing, and rewriting it would be inventing a diagnosis.
func TestDecodeArgsError_NonTypeErrorsPassThrough(t *testing.T) {
	raw := json.RawMessage(`{"operation":`)
	var a struct {
		Operation string `json:"operation"`
	}
	err := json.Unmarshal(raw, &a)
	require.Error(t, err, "the fixture body really is malformed")
	assert.Equal(t, err.Error(), decodeArgsError(raw, err),
		"a non-type decode failure is relayed unchanged")
}

// TestDecodeArgsError_QuotesFallBackToTheKindWhenTheValueIsUnreadable pins the
// degraded path: when the offending field cannot be read back out of the payload
// the message still names the param, the kind sent and the expected form. It is
// the known-positive for rawFieldValue's empty return — without it, "quotes the
// value" would be untested in the one case where it cannot.
func TestDecodeArgsError_QuotesFallBackToTheKindWhenTheValueIsUnreadable(t *testing.T) {
	var target struct {
		Links []string `json:"links"`
	}
	err := json.Unmarshal([]byte(`{"links":"scalar"}`), &target)
	require.Error(t, err)

	// A payload the field lookup cannot read: valid JSON for the decoder's error,
	// but not an object, so rawFieldValue returns "".
	got := decodeArgsError(json.RawMessage(`[]`), err)
	assert.Contains(t, got, "links", "the param is still named")
	assert.Contains(t, got, "a string", "and the KIND that was sent")
	assert.Contains(t, got, "array of strings", "and the accepted form")
	assert.NotContains(t, got, `("`, "but no quoted literal, since none could be read")
}
