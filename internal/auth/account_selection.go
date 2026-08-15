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

// AccountHeaderName is the header the gateway reads to route a bearer call to
// a specific Fulminate account. Absent header => the gateway resolves the
// caller's primary account, which is the pre-selection behavior.
const AccountHeaderName = "Knowledge-Account-Id"

// ErrAccountSelectionRejected is the sentinel returned by IDForRequest once a
// gateway rejection has been observed for the currently stored selection. Both
// transports and the CLI match it with errors.Is.
var ErrAccountSelectionRejected = errors.New("auth: selected Fulminate account was rejected by the gateway")

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
// cross-process invalidation store, and its scope is not a defect. The marker
// is keyed by account id, so it self-clears as soon as a cached read returns a
// different id.
//
// The gateway remains the enforcement authority. This type only lets the
// client decline a call it has already watched fail.
type AccountSelection struct {
	path string
	ttl  time.Duration

	mu        sync.Mutex
	lastCheck time.Time
	id        string
	// invalidID / invalidReason record the gateway rejection observed for
	// that specific account id. Cleared whenever the cached id moves off it.
	invalidID     string
	invalidReason string
	// warnedOnce keeps config-read failures to one WARN per session so a
	// transiently unreadable config does not flood the log. Mirrors
	// AuthState.warnedOnce (state.go:47).
	warnedOnce bool
}

// NewAccountSelection wires an AccountSelection against a config path and TTL.
// A zero ttl falls back to DefaultAccountCheckTTL — tests MAY pass a smaller
// ttl to exercise expiry without sleeping, production wiring uses the default.
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
	if s.invalidID == id {
		return "", fmt.Errorf("%w: account %s: %s — run `knowledge accounts` to list the accounts you can use, then `knowledge account use <id>`",
			ErrAccountSelectionRejected, id, s.invalidReason)
	}
	return id, nil
}

// MarkInvalid records that the gateway rejected id, with the gateway's own
// reason text. Subsequent IDForRequest calls refuse while that id is the
// stored selection. Marking an id that is not (or is no longer) the selection
// is harmless — the marker only ever fires on an exact id match.
func (s *AccountSelection) MarkInvalid(id, reason string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidID = id
	s.invalidReason = reason
}

// currentLocked returns the cached id, refreshing it from the config file when
// the TTL has expired. Clears the rejection marker when the stored id has
// moved off the rejected one. Caller must hold s.mu.
func (s *AccountSelection) currentLocked(ctx context.Context) string {
	_ = ctx // the config read is a local file read; ctx is accepted for symmetry with AuthState
	if !s.lastCheck.IsZero() && time.Since(s.lastCheck) < s.ttl {
		return s.id
	}

	id, err := config.ReadSelectedAccountID(s.path)
	s.lastCheck = time.Now()
	if err != nil {
		// Transient: keep the prior cached value. lastCheck above bumps the
		// TTL so an unreadable config is not re-read on every request.
		s.warnReadFailureOnce(err)
		return s.id
	}
	s.id = id
	if s.invalidID != "" && s.invalidID != id {
		// The user selected a different account (possibly in another
		// terminal): the rejection no longer applies. This is what re-arms a
		// long-lived daemon within one TTL with no IPC.
		s.invalidID = ""
		s.invalidReason = ""
	}
	return s.id
}

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
