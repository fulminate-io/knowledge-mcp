// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// DefaultAccountCheckTTL is the cache window applied to "which Fulminate
// account is this machine routing to?". Mirrors DefaultAuthCheckTTL
// (state.go:20) for the same reason stated there: the answer is consulted on
// every outbound cloud call, and it changes at human speed.
const DefaultAccountCheckTTL = 5 * time.Second

// RejectedAccountTTL is how long a gateway rejection is remembered for ONE
// account before the gateway is asked again.
//
// A 403 is a fact about a MOMENT, not a property of the account: the commonest
// cause is membership lag — the user is added to the account a minute later and
// the gateway would admit them — and a latch with no expiry turned that into a
// daemon that refused the account until it was restarted, printing a remedy
// ("select the original account") that a header-named account does not even
// have. Sixty seconds is the window a membership change lands in at human
// speed. It bounds the wasted round trips to one per minute per account, which
// is the whole benefit the record was ever buying: this is a cache of calls
// already watched fail, never an authorization decision, and the gateway
// re-decides on every call that does go out.
const RejectedAccountTTL = 60 * time.Second

// maxRejectedAccounts bounds the per-account rejection record. The ids that can
// enter it are caller-driven (a /mcp request may name any account), and the
// record is a courtesy cache rather than an authorization decision, so it is
// capped at a small fixed size and dropped whole when the cap is reached.
const maxRejectedAccounts = 32

// AccountHeaderName is the header the gateway reads to route a bearer call to
// a specific Fulminate account. Absent header => the gateway resolves the
// caller's primary account, which is the pre-selection behavior.
const AccountHeaderName = "Knowledge-Account-Id"

// ErrAccountSelectionRejected is the sentinel returned by IDForRequest once a
// gateway rejection has been observed for the currently stored selection. Both
// transports and the CLI match it with errors.Is.
var ErrAccountSelectionRejected = errors.New("auth: selected Fulminate account was rejected by the gateway")

// rejection is one observed gateway refusal: the gateway's own reason and the
// moment it arrived, which is what RejectedAccountTTL is measured from.
type rejection struct {
	reason string
	at     time.Time
}

// AccountSelection answers "which Fulminate account is this process routing
// to?" with a short-lived in-memory cache around config.ReadSelectedAccountID,
// plus a marker recording that the gateway has rejected the current selection.
//
// Concurrency: every method is safe for concurrent use — all state is guarded
// by the receiver mutex. ID/IDForRequest are called from the RPC path.
//
// Mid-session change contract: when a user runs `knowledge account use` in a
// separate process, that command rewrites ~/.knowledge/config. The next read
// after the TTL expires picks up the new id — no IPC, no signal, the config
// file is the shared state. This mirrors how AuthState picks up a `knowledge
// login` performed by another process (state.go:31-36).
//
// THE REJECTION MARKER IS PER-PROCESS AND IN-MEMORY, deliberately. It records
// that a gateway rejection HAS BEEN OBSERVED BY THIS PROCESS; it is not a
// cross-process invalidation store, and its scope is not a defect. It is keyed
// by ACCOUNT ID and holds one entry per account observed rejected, because a
// request can name its own account: a refusal for an account this daemon merely
// asked about must not refuse the account it actually selected. The whole
// record is dropped when a config re-read shows the user deliberately switched
// selections, which is what re-arms a long-lived daemon within one TTL, with no
// IPC.
//
// The gateway remains the enforcement authority. This type only lets the
// client decline a call it has already watched fail.
type AccountSelection struct {
	path string
	ttl  time.Duration

	mu        sync.Mutex
	lastCheck time.Time
	id        string
	// invalid records the gateway's own reason per account id observed
	// rejected, with the moment it was observed: an entry older than
	// RejectedAccountTTL is stale and the gateway is asked again. Bounded by
	// maxRejectedAccounts; see MarkInvalid.
	invalid map[string]rejection
	// warnedOnce keeps config-read failures to one WARN per session so a
	// transiently unreadable config does not flood the log. Mirrors
	// AuthState.warnedOnce (state.go:47).
	warnedOnce bool
	// now is the clock the TTL is measured against. Nil in production, where
	// clockNow falls through to time.Now; tests install a controllable clock
	// via setClockForTest so a cache-window assertion is a statement about the
	// TTL rather than about how busy the machine was. Guarded by mu.
	now func() time.Time
	// sessions is the per-session binding source the request ladder consults
	// between the header and this selection (request_account.go). Nil in a
	// process with no per-session layer — a CLI invocation rather than the
	// daemon — which makes the session rung absent, not degraded. Guarded by mu.
	sessions SessionAccountResolver
	// sessionsGen counts installs of that resolver, so a restore closure can
	// tell whether its own install is still the current one. See
	// SetSessionAccountResolver. Guarded by mu.
	sessionsGen uint64
}

// NewAccountSelection wires an AccountSelection against a config path and TTL.
// A zero ttl falls back to DefaultAccountCheckTTL; production wiring uses the
// default. A test that needs to cross the TTL boundary installs a clock with
// setClockForTest and advances it, rather than shortening the ttl and sleeping.
func NewAccountSelection(path string, ttl time.Duration) *AccountSelection {
	if ttl <= 0 {
		ttl = DefaultAccountCheckTTL
	}
	return &AccountSelection{path: path, ttl: ttl}
}

// ID returns the currently selected account id, or "" when none is stored.
// The config file is re-read at most once per ttl.
//
// A read failure is treated as transient: ID returns the LAST KNOWN value,
// refreshes lastCheck so the TTL is honored (no hammering an unreadable
// config), and emits a single WARN per session. Routing identity must not flap
// because a config read blipped.
func (s *AccountSelection) ID(ctx context.Context) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentLocked(ctx)
}

// IDForRequest is the decision the cloud transports call before every request.
// Three outcomes, and only three:
//
//   - ("", nil)  no selection stored — send no header; the gateway resolves the
//     caller's primary account, exactly as before this feature.
//   - (id, nil)  a selection is stored and has not been rejected — stamp it.
//   - ("", err)  the stored selection has been observed rejected by the
//     gateway — the caller must REFUSE the round trip and surface err.
//
// The refusal implements the CEO directive "client shouldnt try things we know
// would fail as well, just for belt and suspenders" at the same place the
// header is written, so stamping and refusing can never disagree. A locally
// passing selection never skips the gateway's authoritative check.
func (s *AccountSelection) IDForRequest(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.currentLocked(ctx)
	if id == "" {
		return "", nil
	}
	if err := s.rejectionForLocked(id); err != nil {
		return "", err
	}
	return id, nil
}

// RejectionFor reports the gateway rejection observed for THAT account, or nil.
//
// It is the per-account half of the same decision IDForRequest makes for the
// stored selection, and it exists because a request can now name its own
// account: a call bound to account B must be refused when B has been rejected
// and served when it has not, whatever the process selection is or has been
// told about itself. Marking an account that is not the selection is harmless
// for the same reason — the marker only ever fires on an exact id match.
func (s *AccountSelection) RejectionFor(id string) error {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejectionForLocked(id)
}

// rejectionForLocked is the shared body: the record for that account, rendered
// as the caller's error with the gateway's own reason and the remedy. Caller
// must hold s.mu.
func (s *AccountSelection) rejectionForLocked(id string) error {
	seen, rejected := s.invalid[id]
	if !rejected {
		return nil
	}
	if s.clockNow().Sub(seen.at) >= RejectedAccountTTL {
		// Stale: the gateway decides again rather than this process deciding
		// for it forever. Dropped here so the record does not keep an entry
		// nobody will consult against the cap.
		delete(s.invalid, id)
		return nil
	}
	reason := seen.reason
	return fmt.Errorf("%w: account %s: %s — run `knowledge accounts` to list the accounts you can use, then `knowledge account use <id>`",
		ErrAccountSelectionRejected, id, reason)
}

// MarkInvalid records that the gateway rejected id, with the gateway's own
// reason text. Subsequent calls FOR THAT ACCOUNT are refused locally, whether
// they come from the stored selection or from a request that named it.
// Marking an account that is not the selection is harmless — the record only
// ever fires on an exact id match.
//
// The record is BOUNDED. A request may name any account, so the set of ids that
// can be rejected is caller-driven; at the cap the whole record is dropped
// rather than grown. Nothing is lost but the courtesy: this is a cache of
// "calls we have already watched fail", never an authorization decision, and a
// forgotten entry costs one refused round trip to the gateway, which is the
// authority on membership either way.
func (s *AccountSelection) MarkInvalid(id, reason string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invalid == nil {
		s.invalid = make(map[string]rejection, 1)
	}
	if _, known := s.invalid[id]; !known && len(s.invalid) >= maxRejectedAccounts {
		clear(s.invalid)
	}
	s.invalid[id] = rejection{reason: reason, at: s.clockNow()}
}

// currentLocked returns the cached id, refreshing it from the config file when
// the TTL has expired. Clears the rejection marker when the stored id has
// moved off the rejected one. Caller must hold s.mu.
func (s *AccountSelection) currentLocked(ctx context.Context) string {
	_ = ctx // the config read is a local file read; ctx is accepted for symmetry with AuthState
	now := s.clockNow()
	if !s.lastCheck.IsZero() && now.Sub(s.lastCheck) < s.ttl {
		return s.id
	}

	id, err := config.ReadSelectedAccountID(s.path)
	s.lastCheck = s.clockNow()
	if err != nil {
		// Transient: keep the prior cached value. lastCheck above bumps the
		// TTL so an unreadable config is not re-read on every request.
		s.warnReadFailureOnce(err)
		return s.id
	}
	if s.id != "" && s.id != id {
		// The user selected a DIFFERENT account (possibly in another
		// terminal): every rejection observed before that deliberate switch is
		// stale, so the whole record is dropped. This is what re-arms a
		// long-lived daemon within one TTL with no IPC.
		//
		// It compares the previously cached id with the freshly read one,
		// never a record entry with the selection: the record now holds
		// accounts this process merely ASKED about, and the first config read
		// of a process must not wipe a rejection observed before it.
		clear(s.invalid)
	}
	s.id = id
	return s.id
}

// clockNow reads the clock the TTL is measured against: the injected one when a
// test has installed it, wall time otherwise. Mirrors the clock seam already
// used by graphEvictor.clockNow, PropagationLoop.clockNow and
// CollectRuntime.clockNow. Caller must hold s.mu.
func (s *AccountSelection) clockNow() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// setClockForTest installs the clock this selection measures its TTL against.
// TEST ONLY: production never calls it, so clockNow always sees a nil now and
// returns wall time.
//
// It exists because a cache-window assertion made against the wall clock is a
// statement about machine load, not about the TTL: a "still inside the TTL"
// read that a busy runner delays past the TTL observes a correct refresh and
// reports it as a defect.
func (s *AccountSelection) setClockForTest(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// SetClockForTest is setClockForTest for a test in ANOTHER package — the one
// that must watch this daemon follow a selection change across the cache window
// without sleeping through it. TEST ONLY, on the same terms as
// SetSelectedAccountForTest.
func (s *AccountSelection) SetClockForTest(now func() time.Time) { s.setClockForTest(now) }

// warnReadFailureOnce emits a single WARN per session. Caller must hold s.mu.
func (s *AccountSelection) warnReadFailureOnce(err error) {
	if s.warnedOnce {
		return
	}
	s.warnedOnce = true
	slog.Warn("auth: reading the selected Fulminate account failed — routing held at last-known selection",
		"error", err,
		"hint", "check ~/.knowledge/config, or rerun `knowledge account use <id>`",
	)
}

var (
	selectedAccountMu   sync.Mutex
	selectedAccountInst *AccountSelection
)

// SelectedAccount returns the process-wide AccountSelection over the default
// config path, constructing it on first use.
//
// A package-level default rather than a constructor option threaded through
// every transport build is the FAIL-SAFE choice and is the point of the
// design: an option a construction site can forget to pass reproduces exactly
// the silently-split-across-accounts failure this feature exists to prevent,
// whereas a default every Transport reads cannot be forgotten.
//
// A failure to resolve the home directory yields a selection over an empty
// path, which reads as "no selection" — i.e. today's unstamped behavior.
func SelectedAccount() *AccountSelection {
	selectedAccountMu.Lock()
	defer selectedAccountMu.Unlock()
	if selectedAccountInst == nil {
		path, err := config.DefaultPath()
		if err != nil {
			slog.Warn("auth: cannot resolve the config path — no account header will be sent", "error", err)
			path = ""
		}
		selectedAccountInst = NewAccountSelection(path, DefaultAccountCheckTTL)
	}
	return selectedAccountInst
}

// SetSelectedAccountForTest installs sel as the process-wide selection and
// returns a restore closure. Mirrors config.SetForTest (config.go:390).
func SetSelectedAccountForTest(sel *AccountSelection) func() {
	selectedAccountMu.Lock()
	prior := selectedAccountInst
	selectedAccountInst = sel
	selectedAccountMu.Unlock()
	return func() {
		selectedAccountMu.Lock()
		selectedAccountInst = prior
		selectedAccountMu.Unlock()
	}
}
