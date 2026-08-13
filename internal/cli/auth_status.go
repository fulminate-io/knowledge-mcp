// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// ExitNoValidSession is the exit code `knowledge auth-status` returns when it
// determines there is definitively no usable session — not logged in, or the
// stored session expired.
//
// It is deliberately NOT 1. Every unrecognized argv exits 1, so a caller
// probing an older binary that predates this subcommand cannot tell "this
// binary has never heard of auth-status" from "you are not logged in" if both
// are 1. Collapsing those turns a version skew into a confident, wrong refusal
// aimed at operators who may be perfectly logged in. A distinct code makes the
// two cases decidable without matching on prose.
//
// This is a cross-module contract: the bench harness's preflight pins this
// literal. Do not renumber it.
const ExitNoValidSession = 2

// ErrNoValidSession marks the "definitively no usable session" outcome so the
// subcommand dispatcher can map it to [ExitNoValidSession]. The wrapped error
// carries the one-line human reason.
var ErrNoValidSession = errors.New("no valid session")

// authStatusUsage is printed by `knowledge auth-status --help`. The exit-code
// table is part of the documented contract, not a convenience: scripts branch
// on it.
const authStatusUsage = `knowledge auth-status — report whether a usable session is stored

Usage:
  knowledge auth-status

Reads the session published by whichever process owns the login and reports
whether it is currently usable. Makes no network call, writes nothing, and
prints no token material. Works unchanged under
KNOWLEDGE_CREDENTIAL_STORE_READONLY, because it only ever reads.

This answers "am I logged in?" without side effects. It does NOT verify the
session against the server — it reports what is stored and whether it has
expired.

Exit codes (stable contract — scripts depend on these):
  0  a valid session is stored
  2  definitively no valid session: not logged in, or the session expired
  1  indeterminate: the credential store could not be read, or a usage error

An unrecognized subcommand also exits 1, so a caller probing a binary that
may predate this subcommand must treat 1 as "cannot determine" and fall
through to another check — never as a refusal.
`

// AuthStatusCmd implements `knowledge auth-status`. It returns nil when a
// usable session is stored, an error wrapping [ErrNoValidSession] when there
// definitively is not, and any other error when the answer is indeterminate.
//
// It reads through [auth.ReadOnlyTokenSource], which cannot write or refresh,
// so asking the question can never rotate the operator's refresh token or
// disturb the session it is reporting on.
func AuthStatusCmd(args []string) error {
	fs := flag.NewFlagSet("auth-status", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { fmt.Fprint(os.Stdout, authStatusUsage) }

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	ctx := context.Background()
	store, err := openStore()
	if err != nil {
		// Indeterminate, never a refusal: we could not ask the question, which
		// is not the same as learning the answer is no.
		return fmt.Errorf("credential store unavailable: %w", err)
	}

	if _, _, err := auth.NewReadOnlyTokenSource(store).Token(ctx); err != nil {
		return noUsableSession(ctx, store, err)
	}

	// The expiry is not secret and is the one fact an operator wants next.
	// The token itself is never printed, in any form. The read-only source
	// above already parsed this value to decide the session is usable, so a
	// failure to re-read it here costs only the detail, never the verdict.
	if expiry, readErr := store.Get(ctx, auth.KeyAccessTokenExpiry); readErr == nil {
		fmt.Fprintf(os.Stdout, "Logged in — session valid until %s.\n", expiry)
		return nil
	}
	fmt.Fprintln(os.Stdout, "Logged in — session valid.")
	return nil
}

// noUsableSession turns a read-only source failure into the one-line reason a
// human needs. That reason depends on something the session alone cannot
// tell us: whether a login exists at all.
//
// The distinction is not cosmetic. A machine whose owner IS logged in but
// whose owning process has not published a session yet — every machine
// logged in before session publishing existed, until its next refresh — would
// otherwise be told it is logged out, sending an operator to re-authenticate
// a login that is working perfectly. The exit code is the same either way:
// the caller still has no usable session.
func noUsableSession(ctx context.Context, store auth.Store, sessErr error) error {
	switch {
	case errors.Is(sessErr, auth.ErrNoSession):
		if loginExists(ctx, store) {
			return fmt.Errorf("%w: logged in, but no session has been published yet — "+
				"the process that owns the login publishes one on its next refresh; "+
				"`knowledge login` publishes one immediately", ErrNoValidSession)
		}
		return fmt.Errorf("%w: not logged in — run `knowledge login`", ErrNoValidSession)
	case errors.Is(sessErr, auth.ErrSessionExpired):
		if loginExists(ctx, store) {
			return fmt.Errorf("%w: session expired — the process that owns the login "+
				"refreshes it on its next call; `knowledge login` refreshes it now", ErrNoValidSession)
		}
		return fmt.Errorf("%w: session expired and no login remains — run `knowledge login`", ErrNoValidSession)
	default:
		return fmt.Errorf("could not read the stored session: %w", sessErr)
	}
}

// loginExists reports whether a refresh token is stored — the credential that
// mints sessions. Its absence is the only thing that makes "not logged in"
// the honest phrasing. Read-only, like everything else on this path.
func loginExists(ctx context.Context, store auth.Store) bool {
	rt, err := store.Get(ctx, auth.KeyRefreshToken)
	return err == nil && rt != ""
}
