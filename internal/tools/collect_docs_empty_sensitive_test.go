// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect_docs_empty_sensitive_test.go — the guide must state the NARROWED rule
// about defaulted references in an env block.
//
// WHY IT IS A TEST AND NOT A REVIEW NOTE. The guide is what a collector author
// reads before writing their worked entry, and the installer's own documentation
// gate refuses a defaulted reference for a marked name in every shipped document
// including this one. An author who reads only the older, blanket phrasing writes
// a literal where a reference was fine, or a reference where the gate will refuse
// it, and finds out in someone else's CI run.

// TestGuide_StatesTheNarrowedDefaultedReferenceRule pins the sentence, by the
// facts it must carry rather than by its exact words: which names the defaulted
// form is safe for, what a worked entry does for a marked one, and the name of
// the declaration property that carries the answer.
func TestGuide_StatesTheNarrowedDefaultedReferenceRule(t *testing.T) {
	page := guidePage(t)

	require.Contains(t, page, "empty_sensitive",
		"the guide never names the declaration property that decides whether a defaulted reference is safe, "+
			"so an author cannot look their own collector up")

	// The sentence lives in the env-block paragraph, which is the paragraph an
	// author is reading when they write the block — not in a distant appendix.
	const anchor = "A stdio entry's `env` block carries configuration AND credentials"
	idx := strings.Index(page, anchor)
	require.GreaterOrEqual(t, idx, 0, "the guide's env-block paragraph has moved; this row's anchor must move with it")
	paragraph := page[idx:min(idx+1600, len(page))]

	for _, needed := range []string{"empty_sensitive", "${VAR:-}"} {
		assert.Contains(t, paragraph, needed,
			"the env-block paragraph does not mention %q; the narrowed rule belongs where the block is described", needed)
	}

	// AND THE OLD BLANKET CLAIM IS GONE. The guide used to say a provider "can
	// tell them apart" without qualification, which reads as "every provider
	// does"; measured, most do not, and the sentence that replaced it says which
	// ones and how to find out.
	//
	// THE PAGE IS WHITESPACE-NORMALIZED FIRST, and that is the whole point of this
	// row rather than a detail. The claim spans a hard wrap in the source, so an
	// assertion keyed on the exact wrapped literal stops matching the moment the
	// paragraph is re-flowed — which markdown edits do routinely — and the row
	// then passes having observed nothing at all. Collapsing runs of whitespace
	// makes the assertion about the SENTENCE rather than about its line breaks.
	flat := strings.Join(strings.Fields(page), " ")
	// THE SAME-RUN CONTROL, and an absence assertion is worth nothing without one:
	// the normalized page must still contain a nearby sentence from the SAME
	// bullet, so a `flat` that came back empty or mangled cannot make the absence
	// below true by accident.
	require.Contains(t, flat, "A variable set to the empty string is PRESENT and empty",
		"the normalized page does not carry the bullet the retired claim sat in, so the absence assertion below "+
			"would pass whatever the guide says")
	assert.NotContains(t, flat, "Your provider can tell them apart.",
		"the guide still carries the unqualified claim that a provider tells present-and-empty apart from absent")
}
