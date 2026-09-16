// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storeAt builds a store over a scratch root with a controllable clock, and
// returns both. Every test here uses one: no test reads or writes the
// developer's real ~/.knowledge, and no expiry assertion waits on the wall clock.
func storeAt(t *testing.T, root string, now *time.Time) *SessionAccountStore {
	t.Helper()
	s := NewSessionAccountStore(root)
	s.setClockForTest(func() time.Time { return *now })
	return s
}

// TestSessionAccountStore_BindReadOverwriteClear covers the four state
// transitions of one binding, in the order a user drives them.
func TestSessionAccountStore_BindReadOverwriteClear(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s := storeAt(t, root, &now)

	// A session with no binding reads empty, and that is not an error.
	got, err := s.AccountForSession("session-a")
	require.NoError(t, err)
	assert.Empty(t, got, "an unbound session must read empty")

	require.NoError(t, bindOK(t, s, "session-a", "acct_01AAA"))
	got, err = s.AccountForSession("session-a")
	require.NoError(t, err)
	assert.Equal(t, "acct_01AAA", got)

	// Overwrite: the owner's rule is "until overwritten/cleared or 7 days idle".
	require.NoError(t, bindOK(t, s, "session-a", "acct_01BBB"))
	got, err = s.AccountForSession("session-a")
	require.NoError(t, err)
	assert.Equal(t, "acct_01BBB", got, "a second bind replaces the first")

	require.NoError(t, s.Clear("session-a"))
	got, err = s.AccountForSession("session-a")
	require.NoError(t, err)
	assert.Empty(t, got, "a cleared binding is gone")

	// Clearing a session that has none is the requested state, not an error.
	require.NoError(t, s.Clear("session-never-bound"))
}

// TestSessionAccountStore_SurvivesARestartAndSiblingWrites proves the file is
// the state: a SECOND store instance over the same path reads what the first
// wrote (the simulated daemon restart), and binding a third session does not
// disturb the other two.
func TestSessionAccountStore_SurvivesARestartAndSiblingWrites(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	first := storeAt(t, root, &now)

	require.NoError(t, bindOK(t, first, "session-a", "acct_01AAA"))
	require.NoError(t, bindOK(t, first, "session-b", "acct_01BBB"))

	restarted := storeAt(t, root, &now)
	require.NoError(t, bindOK(t, restarted, "session-c", "acct_01CCC"))

	for session, want := range map[string]string{
		"session-a": "acct_01AAA",
		"session-b": "acct_01BBB",
		"session-c": "acct_01CCC",
	} {
		got, err := restarted.AccountForSession(session)
		require.NoError(t, err)
		assert.Equal(t, want, got, "session %s after the restart", session)
	}
}

// TestSessionAccountStore_IdleExpiry pins the 7-day idle window at its boundary,
// from both sides and ON it. The clock is injected; a sleep here would be a
// statement about the machine rather than about the TTL.
func TestSessionAccountStore_IdleExpiry(t *testing.T) {
	// THE WINDOW ITSELF, pinned independently of the constant every row below
	// reads. The length is an owner ruling ("until overwritten/cleared or 7 days
	// idle") and it is PUBLISHED to users in two places, so the constant and the
	// prose must move together or one of them starts lying. Without this line the
	// whole file stays green when the constant is changed, because every case
	// computes its expectation from it.
	// assert, not require: the three legs below are independent claims (the
	// constant, and each of the two published spellings), and a require here
	// would stop the run at the first and leave the other two unobserved.
	assert.Equal(t, 7*24*time.Hour, SessionAccountBindingTTL,
		"the owner's rule is 7 days idle; the manage schema and help both publish it")

	// AND THE TWO PUBLISHED SPELLINGS AGREE WITH IT, each derived from the
	// constant rather than transcribed, so moving the constant moves what these
	// assertions look for and finds neither.
	days := int(SessionAccountBindingTTL / (24 * time.Hour))
	inWords := map[int]string{6: "six days", 7: "seven days", 8: "eight days"}[days]
	require.NotEmpty(t, inWords, "no word spelling known for a %d-day window", days)
	assert.Contains(t, ManageToolDef().Description, fmt.Sprintf("%d days idle", days),
		"the manage schema publishes the window to users and must agree with the constant")
	assert.Contains(t, helpManage, inWords,
		"help(\"manage\") publishes the window to users and must agree with the constant")

	bound := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		idle time.Duration
		want string
	}{
		{"6d23h idle is kept", 6*24*time.Hour + 23*time.Hour, "acct_01AAA"},
		{"exactly 7d is dropped — the window is closed at its end", SessionAccountBindingTTL, ""},
		{"7d+1s is dropped", SessionAccountBindingTTL + time.Second, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			now := bound
			s := storeAt(t, root, &now)
			require.NoError(t, bindOK(t, s, "session-a", "acct_01AAA"))

			now = bound.Add(tc.idle)
			got, err := s.AccountForSession("session-a")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)

			// A dropped binding is gone from the FILE too, not merely from the
			// answer — the next read must not resurrect it.
			raw := readStoreFile(t, root)
			if tc.want == "" {
				assert.NotContains(t, raw, "session-a", "an expired binding must be dropped on read, not just hidden")
			} else {
				assert.Contains(t, raw, "session-a")
			}
		})
	}
}

// TestSessionAccountStore_UseRefreshesTheIdleWindow proves "idle" means UNUSED: a
// session read six days in survives to day eight, which it could not do if only
// the bind moved the stamp.
func TestSessionAccountStore_UseRefreshesTheIdleWindow(t *testing.T) {
	root := t.TempDir()
	bound := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	now := bound
	s := storeAt(t, root, &now)
	require.NoError(t, bindOK(t, s, "session-a", "acct_01AAA"))

	// Used on day six — inside the window, so it resolves AND is touched.
	now = bound.Add(6 * 24 * time.Hour)
	got, err := s.AccountForSession("session-a")
	require.NoError(t, err)
	require.Equal(t, "acct_01AAA", got)

	// Day eight: two days after that use, still well inside the idle window.
	now = bound.Add(8 * 24 * time.Hour)
	got, err = s.AccountForSession("session-a")
	require.NoError(t, err)
	assert.Equal(t, "acct_01AAA", got, "a binding used on day six must survive to day eight")

	// …and the same store with NO use in between expires on schedule, which is
	// the control that keeps the row above from passing on a missing expiry.
	require.NoError(t, bindOK(t, s, "session-b", "acct_01BBB"))
	now = now.Add(SessionAccountBindingTTL + time.Second)
	got, err = s.AccountForSession("session-b")
	require.NoError(t, err)
	assert.Empty(t, got, "an unused binding still expires")
}

// TestSessionAccountStore_ReadsDoNotRewritePerCall bounds the write rate: the
// store sits on the path every tool call takes, so a read inside the touch
// interval must leave the file untouched.
func TestSessionAccountStore_ReadsDoNotRewritePerCall(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s := storeAt(t, root, &now)
	require.NoError(t, bindOK(t, s, "session-a", "acct_01AAA"))

	before := readStoreFile(t, root)
	now = now.Add(sessionAccountTouchInterval / 2)
	for range 5 {
		_, err := s.AccountForSession("session-a")
		require.NoError(t, err)
	}
	assert.Equal(t, before, readStoreFile(t, root), "reads inside the touch interval must not rewrite the file")

	now = now.Add(sessionAccountTouchInterval)
	_, err := s.AccountForSession("session-a")
	require.NoError(t, err)
	assert.NotEqual(t, before, readStoreFile(t, root), "a read past the touch interval must refresh the stamp")
}

// TestSessionAccountStore_CorruptFileIsNeverSilentlyReset is the "no fallback"
// row: an unreadable or malformed store REFUSES the read naming the file, and a
// successful bind is the documented recovery.
func TestSessionAccountStore_CorruptFileIsNeverSilentlyReset(t *testing.T) {
	t.Run("unreadable file refuses the read", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now()
		s := storeAt(t, root, &now)
		require.NoError(t, os.WriteFile(s.Path(), []byte("{ this is not json"), 0o600))

		_, err := s.AccountForSession("session-a")
		require.Error(t, err, "a corrupt store must refuse, never answer empty")
		assert.Contains(t, err.Error(), s.Path(), "the error must name the file to repair")
		assert.Contains(t, err.Error(), "repair or delete it")
		assert.Error(t, s.Check(), "status must be able to report the store unreadable")
	})

	t.Run("malformed record refuses the read", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now()
		s := storeAt(t, root, &now)
		require.NoError(t, os.WriteFile(s.Path(), []byte(`{"session-a":"no-separator-here"}`), 0o600))

		_, err := s.AccountForSession("session-a")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "malformed binding")
	})

	t.Run("a bind replaces a corrupt file — re-binding is the recovery, and it SAYS SO", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now()
		s := storeAt(t, root, &now)
		require.NoError(t, os.WriteFile(s.Path(), []byte("{ this is not json"), 0o600))

		var logged bytes.Buffer
		restore := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
		t.Cleanup(func() { slog.SetDefault(restore) })

		replaced, err := s.Bind("session-a", "acct_01AAA")
		require.NoError(t, err)
		assert.True(t, replaced, "the caller must learn its write wiped every other session's binding")

		// THE OTHER SESSIONS ARE GONE AND NOTHING ELSE WOULD SAY SO: the status
		// key that showed the corruption reads clean from here on.
		assert.Contains(t, logged.String(), "previous bindings discarded")
		assert.Contains(t, logged.String(), s.Path())

		got, err := s.AccountForSession("session-a")
		require.NoError(t, err)
		assert.Equal(t, "acct_01AAA", got)
		require.NoError(t, s.Check())
	})
}

// TestSessionAccountStore_DirectoryIsAParameter pins the path to the data root it
// was handed, and refuses to invent one. A store that resolved its own directory
// from the home dir would write into the developer's real ~/.knowledge from a
// test, and would ignore a daemon's --graph-storage in production.
func TestSessionAccountStore_DirectoryIsAParameter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "non-default-graph-storage")
	s := NewSessionAccountStore(root)

	assert.Equal(t, filepath.Join(root, "session_accounts.json"), s.Path())
	require.NoError(t, bindOK(t, s, "session-a", "acct_01AAA"))
	_, err := os.Stat(s.Path())
	require.NoError(t, err, "the bindings file must land under the root it was given")

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.NotContains(t, s.Path(), filepath.Join(home, ".knowledge"),
		"the store must not resolve a HOME-fixed path")

	t.Run("an unnamed root is an error, never a default", func(t *testing.T) {
		unrooted := NewSessionAccountStore("")
		assert.Empty(t, unrooted.Path())
		_, readErr := unrooted.AccountForSession("session-a")
		require.Error(t, readErr)
		assert.Contains(t, readErr.Error(), "--graph-storage")
		require.Error(t, bindOK(t, unrooted, "session-a", "acct_01AAA"))
		require.Error(t, unrooted.Check())
	})
}

// TestSessionAccountStore_RecordCarriesOnlyTheAccountAndAStamp is the
// credential-handling row: the file on disk holds the session id, the account id
// and a timestamp, and nothing else in any encoding.
func TestSessionAccountStore_RecordCarriesOnlyTheAccountAndAStamp(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s := storeAt(t, root, &now)
	require.NoError(t, bindOK(t, s, "session-a", "acct_01AAA"))

	raw := readStoreFile(t, root)
	for _, forbidden := range []string{
		"Bearer", "bearer", "token", "refresh", "@", "password", "secret",
		"/Users/", "/home/", "transcript", "cwd",
	} {
		assert.NotContains(t, raw, forbidden, "the bindings file must not carry %q", forbidden)
	}

	// The decoded record is exactly the pair, which is the positive half of the
	// same claim: an allowlist proven only by absences would pass on an empty file.
	var decoded map[string]string
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
	require.Len(t, decoded, 1)
	record, err := parseSessionAccountRecord(decoded["session-a"])
	require.NoError(t, err)
	assert.Equal(t, "acct_01AAA", record.accountID)
	assert.Equal(t, now, record.lastUsed.UTC())
	assert.Equal(t, record.accountID+"|"+record.lastUsed.UTC().Format(time.RFC3339Nano), decoded["session-a"],
		"the value is the pair and nothing beside it")
}

// TestSessionAccountStore_ConcurrentBindsLoseNothing drives the mutex: N sessions
// bound in parallel all survive, so no read-modify-write is lost.
func TestSessionAccountStore_ConcurrentBindsLoseNothing(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s := storeAt(t, root, &now)

	const sessions = 16
	var writes sync.WaitGroup
	errs := make([]error, sessions)
	for i := range sessions {
		writes.Go(func() {
			_, errs[i] = s.Bind(fmt.Sprintf("session-%d", i), fmt.Sprintf("acct_%d", i))
		})
	}
	writes.Wait()
	for i, err := range errs {
		require.NoError(t, err, "bind %d", i)
	}
	for i := range sessions {
		got, err := s.AccountForSession(fmt.Sprintf("session-%d", i))
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("acct_%d", i), got, "session %d must survive the concurrent binds", i)
	}
}

// readStoreFile returns the bindings file's raw bytes as a string.
func readStoreFile(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "session_accounts.json"))
	require.NoError(t, err)
	return strings.TrimSpace(string(b))
}

// storeAtReadOnly is storeAt over a root whose DIRECTORY is made read-only
// after the binding is written, so the next persist cannot create its temp file
// while the existing file still reads cleanly. That is the exact shape the
// finding names: a clean read followed by a write that cannot land.
func storeAtReadOnly(t *testing.T, now *time.Time, bind func(*SessionAccountStore)) *SessionAccountStore {
	t.Helper()
	root := t.TempDir()
	s := storeAt(t, root, now)
	bind(s)
	// A DIRECTORY, not a file: gosec's G302 is about file modes, and these two
	// are the search-and-execute bits a directory needs to stay readable while
	// losing the write bit the temp file would need. That is the whole fixture
	// — the store's file still reads cleanly and its next write cannot land.
	require.NoError(t, os.Chmod(root, 0o500|os.ModeDir)) //nolint:gosec // a directory mode, not a file's: read+execute with no write is the fixture
	// Restored before TempDir's own cleanup, which cannot remove a file from a
	// directory it may not write.
	t.Cleanup(func() { _ = os.Chmod(root, 0o750|os.ModeDir) }) //nolint:gosec // same directory mode, restored

	return s
}

// TestSessionAccountStore_AFailedHousekeepingWriteDoesNotFailTheRead is the
// finding's two rows.
//
// THE ROUTING ANSWER IS ALREADY KNOWN when either write is attempted: on the
// expiry branch the binding is expired, so the request falls to the global rung;
// on the touch branch the binding is live, so it IS the answer. The write that
// follows is housekeeping — a prune and a last-used stamp — and a read-only or
// full data root would otherwise break every session-carrying MCP call on the
// daemon for a reason that has nothing to do with which account the request is
// for. Same disposition the resident-bound consolidation took in
// manager_search.go: a failed optimisation must not fail a good operation.
//
// The corrupt-READ refusal is untouched and is asserted alongside, because that
// one really is ambiguous — a binding that exists and cannot be read is a
// different fact from no binding.
func TestSessionAccountStore_AFailedHousekeepingWriteDoesNotFailTheRead(t *testing.T) {
	bound := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	t.Run("the touch branch: a live binding still resolves", func(t *testing.T) {
		now := bound
		s := storeAtReadOnly(t, &now, func(s *SessionAccountStore) {
			require.NoError(t, bindOK(t, s, "session-a", "acct_01AAA"))
		})

		// Past the touch interval, well inside the idle window: the read wants
		// to refresh the stamp and cannot.
		now = bound.Add(sessionAccountTouchInterval * 2)
		got, err := s.AccountForSession("session-a")
		require.NoError(t, err, "a failed stamp refresh must not fail the request")
		assert.Equal(t, "acct_01AAA", got, "the binding is live and is the routing answer")
	})

	t.Run("the expiry branch: an expired binding still falls to the global rung", func(t *testing.T) {
		now := bound
		s := storeAtReadOnly(t, &now, func(s *SessionAccountStore) {
			require.NoError(t, bindOK(t, s, "session-a", "acct_01AAA"))
		})

		now = bound.Add(SessionAccountBindingTTL + time.Second)
		got, err := s.AccountForSession("session-a")
		require.NoError(t, err, "a failed prune must not fail the request")
		assert.Empty(t, got, "the binding is expired, so the request falls to the global rung")
	})

	t.Run("an unreadable file is still a refusal — the ambiguous case is untouched", func(t *testing.T) {
		now := bound
		root := t.TempDir()
		s := storeAt(t, root, &now)
		require.NoError(t, os.WriteFile(s.Path(), []byte("{ not json"), 0o600))

		_, err := s.AccountForSession("session-a")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repair or delete it")
	})
}

// bindOK is Bind with the replaced flag dropped, for the rows whose claim is
// about the binding rather than about a store that had to be replaced. The one
// row that IS about the flag reads it directly.
func bindOK(t *testing.T, s *SessionAccountStore, sessionID, accountID string) error {
	t.Helper()
	_, err := s.Bind(sessionID, accountID)
	return err
}
