// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_update_batch_items_test.go covers the undeclared-key refusal on
// update_batch's items[].
//
// THE DEFECT IT CLOSES: an items[] entry carrying a key the shape does not
// declare — `description` is the one that was reported, but a typo'd `summary` is
// the same class — decodes to nothing, compiles to the id alone, and the call
// RETURNS SUCCESS having written none of it. The caller is told the write
// happened. That is the silent drop "bad input always errors" forbids.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// R7-b. The refuse arm names the offending key and the declared set.
func TestMutate_UpdateBatchRefusesUndeclaredItemKey(t *testing.T) {
	cases := []struct {
		name    string
		args    string
		wantSub []string
	}{
		{
			// R7-d: the refusal is not special-cased to `description`.
			//
			// THE FIXTURE KEY IS A PLAUSIBLE-BUT-UNDECLARED WORD rather than a
			// misspelling. An earlier draft used a misspelled "summary" here and the
			// repository's misspell auto-fixer CORRECTED IT ON DISK — turning the
			// fixture into a declared key and the test into an assertion that a
			// legitimate write is refused. A fixture whose whole point is a wrong
			// spelling cannot live in a tree that spell-corrects on write.
			name:    "an undeclared but correctly spelt key gets the same treatment",
			args:    `{"operation":"update_batch","items":[{"id":"n1","note":"not a field"}]}`,
			wantSub: []string{"items[0]", "note"},
		},
		{
			name:    "the offending item is named by index",
			args:    `{"operation":"update_batch","items":[{"id":"n1","summary":"ok"},{"id":"n2","content":"nope"}]}`,
			wantSub: []string{"items[1]", "content"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &countingMutateCaller{}
			_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
				Name:      "mutate",
				Arguments: json.RawMessage(tc.args),
			})
			require.True(t, res.IsError, "expected a refusal, got: %s", toolResultText(res))
			for _, sub := range tc.wantSub {
				assert.Contains(t, toolResultText(res), sub, "the refusal must name %q", sub)
			}
			assert.Zero(t, fc.mutations, "a refused update_batch must write nothing")
		})
	}
}

// R7-a's control: the DECLARED keys still pass, so the refusals above are not
// satisfiable by a guard that refuses every batch.
//
// EVERY DECLARED KEY IS EXERCISED, not a representative one: the guard reads its
// vocabulary off the schema, so a key present in the decoder but missing from the
// schema would be refused here — which is exactly how `status` was found absent
// from the items[] declaration while engine.batchItem and the proto UpdateItem
// both carried it, and it is what would catch `description` if the proto field
// and the schema declaration ever came apart.
func TestMutate_UpdateBatchDeclaredKeysStillPass(t *testing.T) {
	fc := &countingMutateCaller{}
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name: "mutate",
		Arguments: json.RawMessage(`{"operation":"update_batch","items":[` +
			`{"id":"n1","summary":"s","keywords":"k","status":"active","metadata":{"a":"b"},"description":"a body"},` +
			`{"id":"n2","binary_vector":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","embed_identity":{"model":"m"}}]}`),
	})
	assert.False(t, res.IsError, "a payload of only declared keys must not be refused: %s", toolResultText(res))
}

// The guard, exercised directly on both sides, so the vocabulary it enforces is
// pinned against the schema rather than against the dispatch plumbing.
func TestGuardUpdateBatchItemKeys(t *testing.T) {
	t.Run("every declared key passes", func(t *testing.T) {
		for key := range declaredUpdateBatchItemKeys() {
			payload := `{"operation":"update_batch","items":[{"id":"n1","` + key + `":null}]}`
			assert.NoError(t, guardUpdateBatchItemKeys(json.RawMessage(payload)),
				"the declared key %q must not be refused", key)
		}
	})
	t.Run("the declared set is the decoder's set", func(t *testing.T) {
		// batchItem's json tags, transcribed from engine/compile_mutate.go. The
		// two lists are declared in different modules-worth of code and nothing
		// but this assertion holds them equal; a field added to the decoder and
		// not to the schema is silently unusable, and one added to the schema and
		// not to the decoder is silently dropped.
		decoder := []string{"id", "summary", "keywords", "description", "binary_vector", "metadata", "status", "embed_identity"}
		declared := declaredUpdateBatchItemKeys()
		assert.Len(t, declared, len(decoder))
		for _, key := range decoder {
			assert.True(t, declared[key], "engine.batchItem decodes %q but the items[] schema does not declare it", key)
		}
	})
	t.Run("an undeclared key is refused", func(t *testing.T) {
		assert.Error(t, guardUpdateBatchItemKeys(
			json.RawMessage(`{"operation":"update_batch","items":[{"id":"n1","note":"n"}]}`)))
	})
	// THE KEY THAT LEFT THIS CLASS. `description` was refused because the wire
	// had no carrier for it; it now has one, so the guard must let it through.
	// Asserted here as well as through the dispatch path above, because this is
	// the layer that reads the declared set and a schema regression would show up
	// here first.
	t.Run("description is declared and passes", func(t *testing.T) {
		assert.True(t, declaredUpdateBatchItemKeys()["description"],
			"description has a proto carrier, so the schema must declare it")
		assert.NoError(t, guardUpdateBatchItemKeys(
			json.RawMessage(`{"operation":"update_batch","items":[{"id":"n1","description":"a body"}]}`)))
	})
	t.Run("a payload that does not parse is left to the dispatcher", func(t *testing.T) {
		assert.NoError(t, guardUpdateBatchItemKeys(json.RawMessage(`{not json`)),
			"the guard must not preempt the real unmarshal error with a duplicate")
	})
}

// The determinism rule: two offenders in one payload always name the same one
// first, because the guard walks items[] in the CALLER'S own order and the keys
// of one item in a fixed order.
func TestMutate_UpdateBatchRefusalIsDeterministic(t *testing.T) {
	const twoOffenders = `{"operation":"update_batch","items":[` +
		`{"id":"n1","note":"one"},{"id":"n2","content":"two"}]}`
	var first string
	for i := range 8 {
		fc := &countingMutateCaller{}
		_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
			Name: "mutate", Arguments: json.RawMessage(twoOffenders),
		})
		require.True(t, res.IsError)
		body := toolResultText(res)
		if i == 0 {
			first = body
			assert.Contains(t, body, "items[0]")
			assert.NotContains(t, body, "items[1]")
			continue
		}
		assert.Equal(t, first, body, "the same payload names the same offender on every run")
	}
}

// TestMutate_UpdateBatchRefusalDoesNotContradictItself pins that the refusal does
// not steer a caller away from a capability it lists in the same sentence.
//
// THE DEFECT IT CLOSES was a message that survived a reversal. While description
// was REFUSED, the refusal ended by telling callers to set a description through
// mutate(update) instead. The wire change made description an ACCEPTED items[]
// key, so the same message then listed description among the keys an item accepts
// and, one clause later, told the caller to go elsewhere to set one. It is
// emitted on EVERY undeclared-key refusal, not only on a description.
//
// THE ASSERTION IS ON THE LIVE MESSAGE, not on the source comment, because that
// is what a caller reads and what a comment sweep alone would not have fixed.
func TestMutate_UpdateBatchRefusalDoesNotContradictItself(t *testing.T) {
	fc := &countingMutateCaller{}
	_, res := InterceptMutate(opCtx(), interceptTestDeps{gc: fc}, kgtools.CallToolParams{
		Name:      "mutate",
		Arguments: json.RawMessage(`{"operation":"update_batch","items":[{"id":"n1","note":"not a field"}]}`),
	})
	require.True(t, res.IsError)
	body := toolResultText(res)

	assert.Contains(t, body, "description",
		"description is an accepted items[] key and the refusal lists the accepted set")
	assert.NotContains(t, body, "mutate(update, id:..., description:...)",
		"the refusal must not send a caller elsewhere for a capability it just listed as accepted")
}
