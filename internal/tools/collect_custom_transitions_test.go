// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collect_custom_transitions_test.go — the REGISTRATION LIFECYCLE at the collect
// dispatch: what a collect does after an entry is removed from the config file,
// after it is edited to point at a different provider, and when the type was
// never registered at all.
//
// THE LIFECYCLE IS NOW A HAND EDIT, which is what makes these two tests the
// observation behind "a hand edit takes effect on the next lookup, with no
// daemon restart". Each rewrites the scoped file mid-test, IN THE SAME PROCESS,
// and the next collect is served by whatever the file then says.

// TestCollect_UnknownTypeStillRefused confirms a type that is neither builtin nor
// registered falls through the custom lookup unchanged and is refused as an
// UNKNOWN TYPE.
//
// IT DRIVES A NON-EMPTY ID DELIBERATELY. With an empty id this test reached the
// id guard instead and was a second copy of TestCustomCollect_EmptyIDIsRefused —
// a test named for a behavior it could not observe, which is worse than an
// absent one because a name census reads the row as covered.
func TestCollect_UnknownTypeStillRefused(t *testing.T) {
	deps := newCustomDeps(t)
	handled, res := callCollect(deps, `{"type":"not-a-real-type","id":"some-graph"}`)
	require.True(t, handled)
	require.True(t, res.IsError)
	body := resultText(res)
	assert.Contains(t, body, "not-a-real-type", "the refusal must name the type it could not resolve")
	assert.NotContains(t, body, "'id' is required",
		"an id WAS supplied, so reaching the id guard would mean this test is observing the wrong refusal")
	assert.Nil(t, deps.sink.last(), "an unresolvable type must write nothing")
}

// TestCustomCollect_RemovedEntryIsNoLongerCollectable is the REMOVE transition:
// once the entry is gone from the file the family is withdrawn, and a collect
// naming it is refused rather than served from a remembered record.
//
// THE EDIT HAPPENS IN THE SAME PROCESS, with no restart between the two
// collects, so this is also the negative half of the hand-edit requirement: a
// loader holding an in-memory cache would keep serving the removed family.
//
// The first collect is the same-run known positive — without it, a refusal after
// removal would be equally explained by an entry that never worked.
func TestCustomCollect_RemovedEntryIsNoLongerCollectable(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"`+customStubFamily+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	require.NotNil(t, deps.sink.last(), "known positive: the entry serves a collect while it exists")
	writesBefore := len(deps.sink.results)

	deps.scope.write(t) // the entry is removed from the file, by hand, mid-process

	handled, res = callCollect(deps, `{"type":"`+customStubFamily+`","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError, "a collect naming a deleted registration must be refused: %s", resultText(res))
	assert.Contains(t, resultText(res), customStubFamily, "the refusal must name the type")
	assert.Len(t, deps.sink.results, writesBefore, "a refused collect must write nothing")
}

// TestCustomCollect_AHandEditedEntryThatFailsTheContractNamesITS FILE is ruling
// 5's message half. The check itself already ran on a hand-edited entry — the
// substantive half — and this is about what its refusal tells the operator.
//
// THE FILE IS THE MISSING WORD. Precedence is project then user and the winner
// is taken whole, so the same family name legitimately sits in two files. A
// refusal naming only the family leaves an operator who holds both re-running
// `knowledge collector list` to learn which entry was dialed — the lookup the
// message exists to save.
//
// THE SAME-RUN CONTROL is the collect before the edit: the entry resolves and
// collects, so the refusal after it is about the edit and not about a fixture
// that never worked.
func TestCustomCollect_AHandEditedEntryThatFailsTheContractNamesItsFile(t *testing.T) {
	url := startCustomProvider(t, conformingCustomPayload())
	deps := newCustomDeps(t, customDef(url))

	handled, res := callCollect(deps, `{"type":"`+customStubFamily+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	require.NotNil(t, deps.sink.last(), "known positive: the entry collects before the edit")

	// THE HAND EDIT: the same entry, pointing at a tool the provider does not list.
	broken := customDef(url)
	broken.entry.Tool = "no_such_tool_ful1794"
	deps.scope.write(t, broken)

	handled, res = callCollect(deps, `{"type":"`+customStubFamily+`","id":"board"}`)
	require.True(t, handled)
	require.True(t, res.IsError, "a hand-edited entry is dialed at its first collect: %s", resultText(res))
	body := resultText(res)
	assert.Contains(t, body, deps.scope.path, "the refusal must name the FILE the failing entry lives in")
	assert.Contains(t, body, customStubFamily, "and the entry")
	assert.Contains(t, body, "no_such_tool_ful1794", "and the tool it could not find")
	assert.Contains(t, body, "collect_graph", "and what the provider does list")
}

// TestCustomCollect_EditedEntryUsesTheNewProvider is the EDIT transition, and it
// is the hand-edit requirement's positive arm: the file is re-read on every
// lookup, so an entry edited on disk is served by the NEW provider on the very
// next collect, in the same process, with no restart.
//
// The two providers return distinguishable nodes, so the assertion reads WHICH
// provider served the collect rather than merely that one did.
func TestCustomCollect_EditedEntryUsesTheNewProvider(t *testing.T) {
	firstURL := startCustomProvider(t, map[string]any{
		"nodes":         []any{map[string]any{"id": "FROM-FIRST-PROVIDER", "type": "issue"}},
		"edges":         []any{},
		"walk_complete": true,
	})
	secondURL := startCustomProvider(t, map[string]any{
		"nodes":         []any{map[string]any{"id": "FROM-SECOND-PROVIDER", "type": "issue"}},
		"edges":         []any{},
		"walk_complete": true,
	})
	deps := newCustomDeps(t, customDef(firstURL))

	handled, res := callCollect(deps, `{"type":"`+customStubFamily+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	got := deps.sink.last()
	require.NotNil(t, got)
	require.Len(t, got.Nodes, 1)
	require.Equal(t, "FROM-FIRST-PROVIDER", got.Nodes[0].GetId(),
		"known positive: the first entry is what served the first collect")

	deps.scope.write(t, customDef(secondURL)) // the file is edited to a new provider, by hand

	handled, res = callCollect(deps, `{"type":"`+customStubFamily+`","id":"board"}`)
	require.True(t, handled)
	require.False(t, res.IsError, resultText(res))
	got = deps.sink.last()
	require.NotNil(t, got)
	require.Len(t, got.Nodes, 1)
	assert.Equal(t, "FROM-SECOND-PROVIDER", got.Nodes[0].GetId(),
		"the collect after the edit must be served by the NEW provider, not by a cached session or a remembered record")
}
