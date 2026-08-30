// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTypedUpdate_CriterionNameMirrorsDescriptionOnly fences the one derivation
// the typed update still performs: a criterion's NAME, from the first line of a
// SUPPLIED description. It has no metadata route.
//
// CATCHER: a derive that started stamping name off the merged metadata map would
// overwrite every criterion's displayed label on a command-only edit.
func TestTypedUpdate_CriterionNameMirrorsDescriptionOnly(t *testing.T) {
	node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
		map[string]string{"type": "automated", "command": "go build ./old/..."})
	fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1",
		Metadata: map[string]string{"command": "go build ./new/..."}})
	require.True(t, handled)
	assert.NotContains(t, lastUpdatePlan(t, fc).GetSetFields(), "name",
		"a command-only edit must not re-stamp the criterion name")

	fc2, handled2 := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1",
		Description: "the new observable check"})
	require.True(t, handled2)
	assert.Equal(t, "the new observable check", lastUpdatePlan(t, fc2).GetSetFields()["name"])
}

// TestTypedUpdate_ExplicitSummaryIsUnvalidatedAtAnyLength pins the seam's first
// rule: a caller-supplied summary wins, verbatim and unvalidated. The update path
// applies no cap to it — it is the author's text, forwarded as written.
//
// CATCHER: the summary helper used on the CREATE path word-boundary TRUNCATES
// and reports success. Against a regression that routed the update path through
// it, the caller gets a valid 500-rune prefix and no error, so "no error" alone
// passes — the length equality is the assertion whose failure names the defect.
//
// The fixture is built with strings.Repeat and asserts its intended rune count
// in the test, so it cannot silently drift off the boundary it exists to pin.
func TestTypedUpdate_ExplicitSummaryIsUnvalidatedAtAnyLength(t *testing.T) {
	explicit := strings.Repeat("s", 581)
	node := nodeOf(t, "c1", "criterion", "the suite is green", "the suite is green",
		map[string]string{"type": "automated", "command": "go build ./old/..."})
	fc, handled := runTypedUpdate(t, node, mutateArgs{Operation: "update", ID: "c1",
		Summary:  explicit,
		Metadata: map[string]string{"command": "go build ./new/..."}})
	require.True(t, handled)
	forwarded := lastUpdatePlan(t, fc).GetSetFields()["summary"]
	assert.Equal(t, explicit, forwarded, "an explicit summary passes through verbatim")
	assert.Equal(t, 581, utf8.RuneCountInString(forwarded),
		"an explicit summary must never be truncated — it is unvalidated at any length")
}
