// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

const (
	precedenceHeader  = "22222222-2222-4222-8222-222222222222"
	precedenceSession = "33333333-3333-4333-8333-333333333333"
	precedenceGlobal  = "11111111-1111-4111-8111-111111111111"
)

// installSessionAccount binds a harness session to an account for the duration
// of a test, through the SAME two seams production uses: a resolved harness
// session on the request, and a binding store installed on the process account
// selection. The provider seam this used to drive is gone with the second
// ladder — the session rung is now auth's, and it answers from a real binding.
func installSessionAccount(t *testing.T, id string) {
	t.Helper()
	t.Cleanup(auth.SetHarnessSessionResolverForTest(func(context.Context) auth.HarnessSession {
		if id == "" {
			return auth.HarnessSession{Source: auth.HarnessSourceNone, Reason: auth.HarnessReasonNotResolved}
		}
		return auth.HarnessSession{ID: "session-under-test", Source: auth.HarnessSourceClaudeHook}
	}))
	bindings := map[string]string{}
	if id != "" {
		bindings["session-under-test"] = id
	}
	t.Cleanup(auth.SelectedAccount().SetSessionAccountResolver(mapBindings(bindings)))
}

// mapBindings is a SessionAccountResolver over a fixed map.
type mapBindings map[string]string

func (m mapBindings) AccountForSession(sessionID string) (string, error) { return m[sessionID], nil }

// requestAccountUnderTest is the ladder itself — auth's, the only one — read
// the way every consumer reads it. The error arm is asserted separately (auth's
// own suite); a precedence cell that refused would fail here on the id.
func requestAccountUnderTest(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()
	id, source, err := auth.RequestAccount(ctx)
	require.NoError(t, err)
	return id, string(source)
}

// TestRequestAccountPrecedence is requirement 3's matrix: every combination of
// {header present, session bound, global selected} and the account each one is
// answered for, with the SOURCE asserted and not only the id — two levels can
// name the same account, and an id-only assertion would not say which answered.
func TestRequestAccountPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		header     string
		session    string
		global     string
		wantID     string
		wantSource string
	}{
		{name: "header over session over global", header: precedenceHeader, session: precedenceSession, global: precedenceGlobal, wantID: precedenceHeader, wantSource: AccountSourceHeader},
		{name: "header over session, no global", header: precedenceHeader, session: precedenceSession, wantID: precedenceHeader, wantSource: AccountSourceHeader},
		{name: "header over global, no session", header: precedenceHeader, global: precedenceGlobal, wantID: precedenceHeader, wantSource: AccountSourceHeader},
		{name: "header alone", header: precedenceHeader, wantID: precedenceHeader, wantSource: AccountSourceHeader},
		{name: "session over global", session: precedenceSession, global: precedenceGlobal, wantID: precedenceSession, wantSource: AccountSourceSession},
		{name: "session alone", session: precedenceSession, wantID: precedenceSession, wantSource: AccountSourceSession},
		{name: "global alone", global: precedenceGlobal, wantID: precedenceGlobal, wantSource: AccountSourceGlobal},
		{name: "nothing resolves", wantID: "", wantSource: AccountSourceNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installSelection(t, tc.global)
			installSessionAccount(t, tc.session)
			ctx := t.Context()
			if tc.header != "" {
				ctx = WithRequestAccount(ctx, tc.header, AccountSourceHeader)
			}

			id, source := requestAccountUnderTest(t, ctx)
			assert.Equal(t, tc.wantID, id)
			assert.Equal(t, tc.wantSource, source)
			assert.True(t, accountSourceIsDeclared(source),
				"the accessor answered with %q, which the declared vocabulary %v does not name",
				source, AccountSourcePrecedence())
		})
	}
}

// TestRequestAccountSessionRungIsWired is the PENDING PIN of the header work,
// CONVERTED. It asserted that no session provider was installed and was written
// to fail the moment one was — "the signal to move cells 5 and 6 into the matrix
// above rather than a regression". The session binding has now landed, those
// cells are real in the matrix, and what remains worth pinning is the wiring
// itself: the rung answers from a BINDING STORE, and it answers only for a
// request whose harness session resolved.
func TestRequestAccountSessionRungIsWired(t *testing.T) {
	installSelection(t, precedenceGlobal)
	installSessionAccount(t, precedenceSession)

	id, source := requestAccountUnderTest(t, t.Context())
	require.Equal(t, precedenceSession, id, "the session rung must answer from the installed binding")
	require.Equal(t, AccountSourceSession, source)

	// A call whose harness session did NOT resolve never consults the store, so
	// the same binding does not leak into it: the rung is keyed by the calling
	// session, not by the process.
	installSessionAccount(t, "")
	id, source = requestAccountUnderTest(t, t.Context())
	assert.Equal(t, precedenceGlobal, id, "an unidentified call falls to the global selection")
	assert.Equal(t, AccountSourceGlobal, source)
}

// TestAccountSourcePrecedenceIsTheDeclaredOrder pins the vocabulary itself: the
// declared order is what the resolution follows, and the four words are the
// whole set a status body can carry.
func TestAccountSourcePrecedenceIsTheDeclaredOrder(t *testing.T) {
	assert.Equal(t, []string{AccountSourceHeader, AccountSourceSession, AccountSourceGlobal}, AccountSourcePrecedence())
	assert.False(t, slices.Contains(AccountSourcePrecedence(), AccountSourceNone),
		"none is what is reported when no level answers; it is not a level")

	// The returned slice is a copy: a caller cannot rewrite the precedence.
	AccountSourcePrecedence()[0] = "mutated"
	assert.Equal(t, AccountSourceHeader, AccountSourcePrecedence()[0])
}
