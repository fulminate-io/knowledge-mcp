// SPDX-License-Identifier: Apache-2.0

package kgtypes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRetiredGraphTypeReason_TransformersOnly pins the predicate's discrimination
// in BOTH directions: it answers for the one retired name, and it does NOT answer
// for a surviving builtin or for a name that never existed.
//
// The negative half is not decoration. A predicate that reported ok=true for
// everything would satisfy the positive half alone, and every call site in this
// change would then refuse the whole graph vocabulary — which is a far worse
// failure than the one the predicate exists to prevent.
func TestRetiredGraphTypeReason_TransformersOnly(t *testing.T) {
	reason, ok := RetiredGraphTypeReason("transformers")
	require.True(t, ok, "the retired family is known to the predicate")
	assert.Contains(t, reason, "removed",
		"the sentence names the REMOVAL — a retired name is not a typo, and the message has to say which it is")
	assert.Contains(t, reason, "recipe_body",
		"the client's sentence names the surviving path, because its refusals reach a caller who needs to know what to do instead")

	// SURVIVING BUILTINS: the known-negative drawn through the same call, so a
	// blanket true is impossible to mistake for a correct answer.
	for _, survivor := range BuiltinGraphTypeNames() {
		_, retired := RetiredGraphTypeReason(survivor)
		assert.Falsef(t, retired, "surviving builtin %q must not report as retired", survivor)
	}
	require.NotEmpty(t, BuiltinGraphTypeNames(), "the survivor loop above is not vacuous")

	// NAMES THAT NEVER EXISTED are unknown, not retired. Answering for one would
	// make every typo report as a removed family.
	for _, never := range []string{"", "jira", "transformer", "transformerss", "TRANSFORMERS", "recipes"} {
		_, retired := RetiredGraphTypeReason(never)
		assert.Falsef(t, retired, "%q was never a builtin, so it is unknown rather than retired", never)
	}
}

// TestRetiredGraphTypeReason_IsNotBuiltin is the SEAM assertion between the two
// predicates, and it is the one that would catch a half-done removal.
//
// The retired name must be absent from IsBuiltinGraphType — otherwise the family
// was never really removed — AND present in RetiredGraphTypeReason — otherwise
// the name is simply free, and a user could register a graph type that adopts the
// leftover directory. Either predicate alone is satisfied by exactly the broken
// state the pair exists to exclude.
func TestRetiredGraphTypeReason_IsNotBuiltin(t *testing.T) {
	const retiredName = "transformers"

	assert.False(t, IsBuiltinGraphType(retiredName),
		"the family is REMOVED: the builtin predicate must no longer claim its name")
	assert.NotContains(t, BuiltinGraphTypeNames(), retiredName,
		"and it is gone from the vocabulary a refusal lists, not merely from the predicate")

	_, retired := RetiredGraphTypeReason(retiredName)
	assert.True(t, retired,
		"the freed name stays UNREGISTRABLE: without this the removed family degrades into a custom graph")

	// The two predicates are disjoint over the whole vocabulary, which is the
	// property every call site relies on when it consults one and then the other.
	for _, name := range BuiltinGraphTypeNames() {
		_, retired := RetiredGraphTypeReason(name)
		require.Falsef(t, retired, "%q cannot be both builtin and retired", name)
	}

	// The reason is a sentence rather than a token, so a caller can surface it.
	reason, _ := RetiredGraphTypeReason(retiredName)
	assert.Greater(t, len(strings.Fields(reason)), 5, "the reason is an actionable sentence")
}
