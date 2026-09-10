// SPDX-License-Identifier: Apache-2.0

package corpusscan

import (
	"strings"
	"testing"
)

// TestVocabulary_ContractKeysMatchTheContract is the drift gate on
// contractMetaKeys. The map's KEY is this package's independently-authored
// transcription of a wire spelling; its VALUE is the contract package's own
// constant. A rename inside the contract makes one pair disagree and fails
// here, instead of leaving vocabulary.go describing a vocabulary the parser no
// longer reads.
func TestVocabulary_ContractKeysMatchTheContract(t *testing.T) {
	// The expected count is a hand-pinned constant rather than len() of the map
	// under test: comparing the map against itself would stay green if a whole
	// row were deleted from both the transcription and the walk.
	const wantKeys = 9
	if got := len(contractMetaKeys); got != wantKeys {
		t.Fatalf("contractMetaKeys holds %d keys, want %d — a key was added or dropped without updating this gate", got, wantKeys)
	}
	for wire, constant := range contractMetaKeys {
		if wire != constant {
			t.Errorf("vocabulary drift: this package transcribes %q but the contract constant is %q", wire, constant)
		}
	}
}

// TestVocabulary_CheckVocabularyEnumeratesEveryKey proves checkVocabulary
// renders the whole set rather than a prefix of it, and that it is sorted so
// the operator message is stable run to run.
func TestVocabulary_CheckVocabularyEnumeratesEveryKey(t *testing.T) {
	got := checkVocabulary()
	parts := strings.Split(got, ", ")
	if len(parts) != len(contractMetaKeys) {
		t.Fatalf("checkVocabulary rendered %d keys (%q), want %d", len(parts), got, len(contractMetaKeys))
	}
	for i := 1; i < len(parts); i++ {
		if parts[i-1] >= parts[i] {
			t.Fatalf("checkVocabulary is not sorted: %q", got)
		}
	}
	// A known-positive: the rendering must actually contain a key a reader can
	// check by eye, so an empty-string result cannot satisfy the shape above.
	if !strings.Contains(got, "check_fixture_bad") {
		t.Errorf("checkVocabulary()=%q omits check_fixture_bad", got)
	}
}

// The hand-listed trailing-space walk that stood here is RETIRED, not dropped.
// It named five of the block's constants and judged them against a list its
// author retyped, so a constant added to the block was outside it by default —
// which is exactly what happened. Its property is now asserted over EVERY
// declared title by TestVocabulary_LockedTitleNamingRuleDecidesTheTrailingSpace,
// which reads the block out of the source; see locked_titles_census_test.go.
