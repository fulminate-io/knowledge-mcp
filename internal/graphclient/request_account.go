// SPDX-License-Identifier: Apache-2.0

// request_account.go — which Fulminate account ONE request is answered for.
//
// The process has a selected account (auth.AccountSelection, read from
// ~/.knowledge/config) and that used to be the only answer available: every
// call the daemon served, whatever its origin, routed to it. A browser page
// signed into two accounts needs one REQUEST to reach the other one, so the
// answer becomes per-request and the levels are ordered, highest first:
//
//	header   an inbound Knowledge-Account-Id on this /mcp request
//	session  the calling MCP session's binding
//	global   the process-wide config selection
//	none     nothing resolves — no account
//
// A MISS at a level falls to the next one. A FAILURE at the header level never
// falls anywhere: a malformed header is refused at the transport (handlePOST),
// and an account the gateway refuses surfaces that refusal, because a fallback
// would answer the request for an account the caller did not ask for. The
// header names an account, it never grants one — membership and subscription
// are the gateway's to enforce, on every route, and the client's binding only
// decides which account it asks about.
package graphclient

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// The account_source vocabulary reported by manage(status) and by
// RequestAccount. One vocabulary across the account-switching work, so a reader
// of a status body never meets an undeclared word.
//
// THEY ARE auth's WORDS, not a second set. The resolution itself lives in
// package auth (auth.RequestAccount), because the two stamping chokepoints are
// the Connect interceptor here and the raw /v1/sync transport there, and auth is
// the only package both can reach. These constants are the same values under the
// names this package's readers already use.
const (
	AccountSourceHeader  = string(auth.AccountSourceHeader)
	AccountSourceSession = string(auth.AccountSourceSession)
	AccountSourceGlobal  = string(auth.AccountSourceGlobal)
	AccountSourceNone    = string(auth.AccountSourceNone)
)

// AccountSourcePrecedence returns the resolution levels, highest first. It is
// the DECLARED vocabulary a caller (or a test pinning the contract) reads
// rather than re-typing the four words; AccountSourceNone is not a level, it is
// what is reported when no level answers.
func AccountSourcePrecedence() []string {
	return []string{AccountSourceHeader, AccountSourceSession, AccountSourceGlobal}
}

type requestAccountKey struct{}

// requestAccount is the resolved account of one request plus the level it came
// from. Both travel together because the two are reported together: an id alone
// cannot distinguish a header-bound request from an identically-selected global
// one, which is exactly the distinction the web pages label.
type requestAccount struct {
	id     string
	source string
	// header is the account the inbound header named, whatever the resolution
	// then did with it, and reason says what that was when the answer is not
	// the header's account. A logged-out daemon serves such a request LOCALLY,
	// so reporting the header's account as the request's account would be a
	// wrong-cause claim — a web page would render a cloud account's name over
	// local data — while saying nothing at all would leave the caller guessing
	// why its header did not take.
	header string
	reason string
}

// AccountReasonLoggedOut is the reason a header-carrying request on a
// logged-out daemon reports. It is the account-side twin of the harness
// session's reason vocabulary: a closed phrase, named so a reader can tell
// "you sent no header" from "your header was ignored, and here is why".
const AccountReasonLoggedOut = "header ignored: logged out"

// WithRequestAccount carries an account resolved for THIS request, and the
// level it was resolved at, on ctx. Request-scoped identity rides the context
// from the handler to every boundary; it is never a struct field and never a
// package variable, so two concurrent requests for two accounts cannot observe
// each other's binding.
func WithRequestAccount(ctx context.Context, id, source string) context.Context {
	// The ladder's own carrier, so auth.RequestAccount resolves the header rung
	// from the same fact this record reports. One ladder: this function records
	// what the transport read, it does not decide anything.
	ctx = auth.WithHeaderAccount(ctx, id)
	return context.WithValue(ctx, requestAccountKey{}, requestAccount{id: id, source: source, header: id})
}

// WithIgnoredAccountHeader records that a request CARRIED an account header
// which did not decide its account, and why. The request resolves to no account
// at all rather than falling through to the session or the global selection:
// the caller asked for a specific account and did not get it, and reporting
// some other account as the one it was answered for is the wrong-cause claim
// this carrier exists to prevent.
func WithIgnoredAccountHeader(ctx context.Context, header, reason string) context.Context {
	ctx = auth.WithIgnoredHeaderAccount(ctx, header, reason)
	return context.WithValue(ctx, requestAccountKey{}, requestAccount{
		source: AccountSourceNone,
		header: header,
		reason: reason,
	})
}

// RequestAccountReason reports why this request's account is not the one its
// header named, or "" when there is nothing to explain.
func RequestAccountReason(ctx context.Context) string {
	bound, _ := ctx.Value(requestAccountKey{}).(requestAccount)
	return bound.reason
}

// requestAccountHeader reports the account the inbound header named, whatever
// the resolution did with it. BindStorage reads it to name the header in the
// refusal a logged-out cloud call gets.
func requestAccountHeader(ctx context.Context) string {
	bound, _ := ctx.Value(requestAccountKey{}).(requestAccount)
	return bound.header
}

// AccountHeaderFromRequest reads the inbound account header off an /mcp request
// and returns its CANONICAL account id, or "" when the header is absent.
//
// Bad input errors; nothing is coerced. Present-and-empty, sent more than once,
// and anything that is not a UUID are all refused with the header named, rather
// than treated as absent — an absent header falls to the session and global
// levels, and silently treating a typo as absence would answer the request for
// a DIFFERENT account than the caller asked for.
//
// The accepted value is canonicalized through uuid.Parse: the gateway parses
// the same header the same way and is therefore case-insensitive, while this
// client uses the id as a map key (Router.boundCloud) and as the digest input
// of a segment cache directory. Two spellings of one account must not key two
// clients, two cache directories or two entries against the destination cap.
func AccountHeaderFromRequest(r *http.Request) (string, error) {
	values := r.Header.Values(auth.AccountHeaderName)
	if len(values) == 0 {
		return "", nil
	}
	if len(values) > 1 {
		return "", fmt.Errorf("%s was sent %d times; send it once with the account id the request is for",
			auth.AccountHeaderName, len(values))
	}
	id, err := uuid.Parse(values[0])
	if err != nil {
		return "", fmt.Errorf("%s %q is not an account id: %w", auth.AccountHeaderName, values[0], err)
	}
	return id.String(), nil
}

// accountSourceIsDeclared reports whether source is one of the levels
// AccountSourcePrecedence names, or the none report.
func accountSourceIsDeclared(source string) bool {
	return source == AccountSourceNone || slices.Contains(AccountSourcePrecedence(), source)
}
