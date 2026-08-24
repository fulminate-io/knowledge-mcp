// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCalleeProfileCoverage RE-DERIVES the declared/consumed partition from the
// registry every time it runs, rather than trusting a list.
//
// The callee profile table is an OVERRIDE table, so a language with no row
// silently takes the zero profile — which is the right default for a shell and
// the wrong one for a language whose callees are names. This test makes that
// choice EXPLICIT: every registered language carrying a non-empty Calls query
// must either hold a row or be named in the no-row list, so adding a language
// cannot quietly opt it out of the whole ruleset.
//
// It also asserts the ReceiverWrappers / ReceiverArgStop PARITY. Those two
// fields are one knob: a wrapper list with no stop kind is a walk that climbs
// out of an argument list into the caller and deletes legitimate
// argument-position calls, which is a defect no other gate in this package
// would notice.
func TestCalleeProfileCoverage(t *testing.T) {
	noRow := make(map[Language]bool, len(calleeProfileNoRow))
	for _, lang := range calleeProfileNoRow {
		noRow[lang] = true
	}

	rows := make(map[Language]bool, len(calleeProfiledLanguages()))
	for _, lang := range calleeProfiledLanguages() {
		rows[lang] = true
		assert.False(t, noRow[lang],
			"%s is named in the no-row list AND carries a row", lang)
	}

	// THE FLOOR IS WHAT STOPS THIS PASSING VACUOUSLY: a walk over an empty
	// registry, or one whose Queries() came back nil, would assert nothing at
	// all. The same nineteen languages the capture-name census counts carry a
	// non-empty Calls query today.
	withCalls := 0
	for lang, entry := range registry {
		qs := entry.Queries()
		if qs == nil || qs.Calls == "" {
			continue
		}
		withCalls++
		assert.True(t, rows[lang] || noRow[lang],
			"%s has a Calls query but neither a callee profile row nor a place in the no-row list", lang)
	}
	require.GreaterOrEqual(t, withCalls, 19,
		"the partition must have walked the registry, not an empty set")

	// Parity, and a decline-knob sanity check: every gated knob is inert
	// without DeclineNonName, so a row carrying one without it is dead.
	for _, lang := range calleeProfiledLanguages() {
		prof := calleeProfileFor(lang)
		assert.Equal(t, len(prof.ReceiverWrappers) > 0, len(prof.ReceiverArgStop) > 0,
			"%s must set ReceiverWrappers and ReceiverArgStop together or not at all", lang)
		if len(prof.ReceiverWrappers) > 0 {
			for _, k := range prof.ReceiverWrappers {
				assert.NotContains(t, prof.ReceiverArgStop, k,
					"%s lists %q as both a receiver wrapper and an argument stop", lang, k)
			}
		}
		assert.NotEmpty(t, prof.ChainFollow,
			"%s must take the non-zero ChainFollow default", lang)
	}

	names := make([]string, 0, len(calleeProfileNoRow))
	for _, lang := range calleeProfileNoRow {
		names = append(names, string(lang))
	}
	sort.Strings(names)
	t.Logf("callee profile coverage: %d rows, %d languages with a Calls query, no-row languages: %v",
		len(rows), withCalls, names)
}
