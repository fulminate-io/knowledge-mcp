// SPDX-License-Identifier: Apache-2.0

// session_account_store.go — a MACHINE-LOCAL store of which Fulminate account
// each harness session's calls are routed to, at
// <graph-storage>/session_accounts.json (beside the segment cache, the daemon's
// pid and its logs).
//
// IT FOLLOWS --graph-storage, NOT $HOME, and that is the one way it differs from
// the two stores whose shape it copies. repo_manifest.go and sync_watermark.go
// each resolve their own path from os.UserHomeDir, so they land in ~/.knowledge
// whatever the daemon was started with; this store belongs to ONE daemon's data
// root, the way the segment cache does (bootstrap/client_segment.go), so a second
// daemon under a second --graph-storage keeps its own bindings and a test never
// writes into the developer's real one. The directory is a CONSTRUCTOR PARAMETER
// with no default: an unnamed directory is an error, never a fallback to $HOME.
//
// LOAD-BEARING INVARIANT: this file is NEVER persisted to the cloud or any synced
// store, for the reason sync_watermark.go:9-15 states for its own — the value
// describes THIS machine's relationship to a remote, not a fact about the world.
//
// THE RECORD IS TWO FIELDS AND NOTHING ELSE: an account id and a last-used
// stamp. No bearer token, no refresh token, no email, no workspace path, no
// transcript path. A session binding is a routing fact; anything else in this
// file would be a credential at rest that nothing here needs.

package tools

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// SessionAccountBindingTTL is how long a binding survives without being used.
// The owner's rule: a binding lives "until overwritten/cleared or 7 days idle".
const SessionAccountBindingTTL = 7 * 24 * time.Hour

// sessionAccountTouchInterval bounds how often a READ rewrites the file to
// refresh a binding's last-used stamp.
//
// IDLE MEANS UNUSED, so a read is a use and must move the stamp — but this store
// is read on the path every MCP tool call takes, and a rewrite per call would put
// a temp-file-plus-rename on that path for a value that changes at human speed.
// Refreshing only once the stamp is older than this bounds the writes to one per
// session per interval while keeping the expiry honest to within it.
const sessionAccountTouchInterval = time.Hour

// sessionAccountStoreFile is the file name under the daemon's data root.
const sessionAccountStoreFile = "session_accounts.json"

// SessionAccountStore is a mutex-guarded reader/writer over the JSON map
// {"<session id>": "<account id>|<RFC3339Nano last used>"} at `path`. Every read
// re-reads from disk, so a binding written by a concurrent request is observed
// and a daemon restart loses nothing.
type SessionAccountStore struct {
	mu   sync.Mutex
	path string
	// now is the clock the idle window is measured against. Nil in production;
	// tests install one so a 7-day assertion is a statement about the TTL rather
	// than about the wall clock. Guarded by mu.
	now func() time.Time
}

// NewSessionAccountStore builds the store under one daemon data root — the
// tilde-expanded --graph-storage value, the same root the segment cache follows.
//
// An EMPTY root yields a store that errors on every operation rather than one
// that picks a directory of its own. A caller that forgot to name the root must
// find out at the first binding, not discover months later that the daemon has
// been writing into $HOME.
func NewSessionAccountStore(root string) *SessionAccountStore {
	if root == "" {
		return &SessionAccountStore{}
	}
	return &SessionAccountStore{path: filepath.Join(root, sessionAccountStoreFile)}
}

// Path reports the file the bindings live in, or "" for an unconfigured store.
func (s *SessionAccountStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// errNoSessionStoreRoot is returned by every operation on a store built without
// a data root.
var errNoSessionStoreRoot = errors.New("session bindings have no directory: the daemon was started without a --graph-storage data root")

// unreadable wraps a read failure in the message the user acts on. The file is
// NEVER replaced to recover from one: silently resetting would drop every
// session's binding and route their writes to the machine-wide account without
// anyone being told.
func (s *SessionAccountStore) unreadable(err error) error {
	return fmt.Errorf("%w at %s: repair or delete it: %w", auth.ErrSessionBindingsUnreadable, s.path, err)
}

// AccountForSession reports the account bound to one harness session, or "" when
// that session has no live binding.
//
// It DROPS an expired binding as it reads (lazy convergence — no timer and no
// startup sweep) and refreshes the last-used stamp of a live one that has not
// been touched for an interval, persisting both in the same rewrite.
//
// An unreadable or malformed file is an ERROR, not an empty map: see unreadable.
// A failed HOUSEKEEPING WRITE is not — see persistHousekeeping.
func (s *SessionAccountStore) AccountForSession(sessionID string) (string, error) {
	if s == nil || s.path == "" {
		return "", errNoSessionStoreRoot
	}
	if sessionID == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := readLocalJSONMap(s.path)
	if err != nil {
		return "", s.unreadable(err)
	}
	raw, present := entries[sessionID]
	if !present {
		return "", nil
	}
	record, err := parseSessionAccountRecord(raw)
	if err != nil {
		return "", s.unreadable(fmt.Errorf("session %q: %w", sessionID, err))
	}
	now := s.clockNow()
	if now.Sub(record.lastUsed) >= SessionAccountBindingTTL {
		delete(entries, sessionID)
		s.persistHousekeeping(entries, now, "expiry prune")
		return "", nil
	}
	if now.Sub(record.lastUsed) < sessionAccountTouchInterval {
		return record.accountID, nil
	}
	entries[sessionID] = formatSessionAccountRecord(record.accountID, now)
	s.persistHousekeeping(entries, now, "touch")
	return record.accountID, nil
}

// persistHousekeeping writes the pruned or refreshed entries and reports a
// failure to the LOG rather than to the caller.
//
// A FAILED OPTIMISATION MUST NOT FAIL A GOOD OPERATION — the same disposition
// the resident-bound consolidation takes in manager_search.go, and the reason is
// the same here: by the time either write is attempted the routing answer is
// already unambiguous. On the expiry branch the binding is expired, so the
// request falls to the global rung; on the touch branch the binding is live, so
// it IS the answer. The write that follows is a prune and a last-used stamp,
// and returning its error would break every session-carrying call on the daemon
// — over a read-only or full data root — for a reason that has nothing to do
// with which account the request is for.
//
// IT IS NOT THE CASE unreadable DEFENDS. That one is a CORRUPT file, where "a
// binding exists but cannot be read" really is a different fact from "no
// binding", and it stays a refusal. A clean read followed by a failed write is
// not ambiguous, so it is logged loudly and the answer is served.
func (s *SessionAccountStore) persistHousekeeping(entries map[string]string, now time.Time, branch string) {
	if err := s.persist(entries, now); err != nil {
		slog.Error("tools: the session binding store could not be updated — routing unchanged; the stamp/prune is retried at the next read",
			"path", s.path, "branch", branch, "error", err)
	}
}

// Bind routes one harness session's calls to an account, replacing whatever that
// session was bound to before.
// It reports whether the write REPLACED an unreadable store. That is the
// documented recovery from a corrupt file, and it is not silent: the other
// sessions' bindings went with it, so the caller says so in its response and the
// log names the file.
func (s *SessionAccountStore) Bind(sessionID, accountID string) (replaced bool, err error) {
	if s == nil || s.path == "" {
		return false, errNoSessionStoreRoot
	}
	if sessionID == "" || accountID == "" {
		return false, errors.New("a session binding needs both a session id and an account id")
	}
	return s.rewrite(func(entries map[string]string, now time.Time) {
		entries[sessionID] = formatSessionAccountRecord(accountID, now)
	})
}

// Clear removes one session's binding. Clearing a session that has none is not an
// error — the requested state is the state that results.
func (s *SessionAccountStore) Clear(sessionID string) error {
	if s == nil || s.path == "" {
		return errNoSessionStoreRoot
	}
	_, err := s.rewrite(func(entries map[string]string, _ time.Time) {
		delete(entries, sessionID)
	})
	return err
}

// Check reports whether the store is readable, for manage(status) to say so.
// A store that has never been written is readable (there is nothing wrong with an
// absent file); a corrupt one names itself.
func (s *SessionAccountStore) Check() error {
	if s == nil || s.path == "" {
		return errNoSessionStoreRoot
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := readLocalJSONMap(s.path)
	if err != nil {
		return s.unreadable(err)
	}
	for sessionID, raw := range entries {
		if _, err := parseSessionAccountRecord(raw); err != nil {
			return s.unreadable(fmt.Errorf("session %q: %w", sessionID, err))
		}
	}
	return nil
}

// rewrite applies mutate to the current entries and writes them back atomically.
// A WRITE REPLACES AN UNREADABLE FILE: re-binding is the documented recovery from
// a corrupt store, so a write is the one operation that must not be blocked by
// the state it repairs.
func (s *SessionAccountStore) rewrite(mutate func(map[string]string, time.Time)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := readLocalJSONMap(s.path)
	replaced := false
	if err != nil {
		// THE OTHER SESSIONS' BINDINGS GO WITH IT, and that is said out loud.
		// While the store was corrupt every session-carrying call was refused;
		// this write makes the daemon serve again, and every session but this
		// one silently drops to the machine-wide account. The only surface that
		// would have shown the corruption — manage(status)'s
		// session_binding_store_error — is clean from here on, so the log line
		// and the caller's own response are what is left to say it happened.
		replaced = true
		slog.Error("tools: the session binding store was unreadable and has been replaced — previous bindings discarded; other sessions now resolve to the global selection until they re-bind",
			"path", s.path, "error", err)
		entries = map[string]string{}
	}
	now := s.clockNow()
	mutate(entries, now)
	return replaced, s.persist(entries, now)
}

// persist prunes every expired or unparseable binding and writes the rest.
// Caller must hold s.mu.
func (s *SessionAccountStore) persist(entries map[string]string, now time.Time) error {
	for sessionID, raw := range entries {
		record, err := parseSessionAccountRecord(raw)
		if err != nil || now.Sub(record.lastUsed) >= SessionAccountBindingTTL {
			delete(entries, sessionID)
		}
	}
	return writeLocalJSONMapAtomic(s.path, "session_accounts-*.json.tmp", "session bindings", entries)
}

// clockNow reads the clock the idle window is measured against. Caller must hold
// s.mu.
func (s *SessionAccountStore) clockNow() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// setClockForTest installs the clock the idle window is measured against.
// TEST ONLY: production never calls it, so clockNow always sees a nil now.
func (s *SessionAccountStore) setClockForTest(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// sessionAccountRecord is one binding: the account and when it was last used.
type sessionAccountRecord struct {
	accountID string
	lastUsed  time.Time
}

// formatSessionAccountRecord encodes the pair as "<account id>|<RFC3339Nano>".
// The map primitives this store shares with the repo manifest hold string values,
// and a two-field record encoded into one is what keeps this store from becoming
// a third hand-written file writer with its own missing-file and rename rules.
func formatSessionAccountRecord(accountID string, lastUsed time.Time) string {
	return accountID + "|" + lastUsed.UTC().Format(time.RFC3339Nano)
}

// parseSessionAccountRecord decodes one value. A value it cannot read is an
// error, never a skipped entry: a binding nobody can read is exactly the state
// the caller must be told about.
func parseSessionAccountRecord(raw string) (sessionAccountRecord, error) {
	accountID, stamp, found := strings.Cut(raw, "|")
	if !found || accountID == "" || stamp == "" {
		return sessionAccountRecord{}, fmt.Errorf("malformed binding %q: want \"<account id>|<RFC3339 timestamp>\"", raw)
	}
	lastUsed, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return sessionAccountRecord{}, fmt.Errorf("malformed binding timestamp %q: %w", stamp, err)
	}
	return sessionAccountRecord{accountID: accountID, lastUsed: lastUsed}, nil
}
