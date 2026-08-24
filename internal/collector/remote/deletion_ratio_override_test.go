// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// unsetForTest removes an env var for the duration of a test. t.Setenv restores
// whatever was there before, including absence, so removing it here is safe.
func unsetForTest(key string) error { return os.Unsetenv(key) }

// TestCollectDeletionRatioOverride_LeverVocabulary pins what the override lever
// accepts, and it exists because nothing else referenced the function or the
// variable — the lever could have been deleted outright and every other test
// stayed green.
//
// THE OFF CASES CARRY THE WEIGHT. This lever RELAXES a refusal, so its default
// must survive every way an operator can get the value wrong: unset, empty, a
// falsey word, or a typo. A regression to the tempting `v != ""` shape turns
// "0", "false", "off" and "garbage" into ON and reddens four rows here.
//
// UNSET AND EMPTY ARE BOTH OFF but reach that answer by different paths —
// LookupEnv's ok=false for the first, the switch default for the second — so
// both are listed rather than assumed equivalent.
func TestCollectDeletionRatioOverride_LeverVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   bool
		value string
		want  bool
	}{
		{name: "unset", set: false, want: false},
		{name: "empty", set: true, value: "", want: false},
		{name: "zero", set: true, value: "0", want: false},
		{name: "false", set: true, value: "false", want: false},
		{name: "off", set: true, value: "off", want: false},
		{name: "garbage", set: true, value: "garbage", want: false},
		{name: "one", set: true, value: "1", want: true},
		{name: "on", set: true, value: "on", want: true},
		{name: "true", set: true, value: "true", want: true},
		{name: "yes", set: true, value: "yes", want: true},
		// Trimmed AND folded: an operator who exports the variable with stray
		// whitespace or shouting still gets the lever they asked for.
		{name: "padded upper TRUE", set: true, value: " TRUE ", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(collectDeletionRatioOverrideEnv, tc.value)
			} else {
				// t.Setenv cannot express "absent", so unset it explicitly and let
				// the test framework restore it.
				t.Setenv(collectDeletionRatioOverrideEnv, "")
				require.NoError(t, unsetForTest(collectDeletionRatioOverrideEnv))
			}
			require.Equal(t, tc.want, collectDeletionRatioOverride(),
				"lever value %q must resolve to %v", tc.value, tc.want)
		})
	}
}
