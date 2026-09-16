// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// newAccountInterceptor returns the outbound interceptor that stamps the
// Fulminate account THIS REQUEST resolved to on every cloud RPC, and refuses an
// account the gateway has already been observed to reject.
//
// The account is the bound destination's when the call carries one — the request
// ladder resolved it from the header, the calling session's binding or the
// machine-wide selection before the destination was built — and the selection's
// otherwise.
//
// It is the Connect-side half of the two stamping chokepoints; the raw
// /v1/sync surface is stamped inside auth.Transport.issueBytes. Both resolve
// through the same auth.AccountSelection and the same ladder, so stamping and
// refusing cannot disagree between them.
//
// No selection stored => NO header at all, rather than an empty one: the
// gateway then resolves the caller's primary account, exactly as it did before
// this feature existed.
//
// REQUEST-SIDE ONLY. This interceptor stamps and refuses BEFORE dispatch; it
// does NOT classify responses. A Connect interceptor cannot see the gateway's
// rejection body — connect-go parses a non-200 body into a wire error carrying
// only code/message/details, so the gateway's error/error_description body
// unmarshals with an empty message. Response classification therefore lives in
// bearerRoundTripper.RoundTrip, the one place on this chain holding the raw
// *http.Response.
//
// It is installed on the CLOUD client only. The local h2c server on 127.0.0.1
// is single-tenant and has no account concept, and a refusal there would turn
// a cloud-side rejection into a total local outage.
//
// THE BOUND DESTINATION IS THE SOURCE OF THE STAMP, not a value to agree with
// the selection. A request can name its own account (an inbound
// Knowledge-Account-Id on /mcp, or a qualified reference), and that account is
// what its RPCs must carry; comparing it to the process selection and refusing
// on a difference is what made a per-request account impossible. What survives
// of the old gate is its fail-closed half: a cloud RPC bound to a LOCAL
// destination is refused, because that binding names no cloud account at all.
//
// Membership and subscription stay the gateway's to enforce on every route. The
// binding decides WHICH account this client asks about; it grants nothing, and
// nothing here treats a caller-named account as authorized.
func newAccountInterceptor(sel *auth.AccountSelection) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			id, err := accountToStamp(ctx, sel)
			if err != nil {
				return nil, connect.NewError(accountFaultCode(err), err)
			}
			if id != "" {
				req.Header().Set(auth.AccountHeaderName, id)
			}
			return next(ctx, req)
		}
	})
}

// accountFaultCode classifies a resolution failure for the caller's log.
//
// A REJECTED ACCOUNT IS A PERMISSION DECISION: the gateway refused it and the
// client is declining to re-ask. A BINDING STORE THIS DAEMON CANNOT READ IS NOT
// — it is this process failing to read its own state, and reporting it as
// permission denied sends the user to look at their account membership for a
// fault that lives in a file on their own disk.
func accountFaultCode(err error) connect.Code {
	if errors.Is(err, auth.ErrSessionBindingsUnreadable) {
		return connect.CodeInternal
	}
	return connect.CodePermissionDenied
}

// accountToStamp resolves the account one outbound cloud RPC must carry, or an
// error the caller turns into a local refusal. An empty id and a nil error mean
// "send no header at all", which is how a client with nothing selected has always
// behaved and what makes the gateway resolve the caller's primary account.
//
// A BOUND DESTINATION IS THE SOURCE OF THE STAMP, not something to check the
// machine-wide selection against. Its account was already resolved by the request
// ladder — header, then the calling session's binding, then the selection — so
// re-deriving it here from the selection would refuse every call a header or a
// session binding routed elsewhere, which is exactly what this used to do. What
// remains of the old gate is its fail-closed half, the consistency statement a
// cloud client can make on its own: a cloud RPC bound to a LOCAL destination is
// refused, because that binding names no cloud account at all.
//
// A DESTINATION CARRYING NO ACCOUNT FALLS THROUGH to THE LADDER rather than being
// refused. That state is "nothing resolved an account", which the surfaces that
// REQUIRE one refuse for themselves with an actionable message; refusing it here
// reported an account switch that never happened. It falls through to the ladder
// and not to the selection so that the invariant this change rests on — one
// ladder, read by both stamping chokepoints — has no exception in it. Today the
// two answers are the same for every caller that can reach the line: a user's MCP
// call binds before dispatch and is refused outright if it bound no account, so
// what arrives here is background work carrying no harness session, for which the
// ladder resolves the selection anyway. It stops being the same answer the moment
// another destination producer lands, and the whole point of one ladder is that
// nobody has to remember this site then.
func accountToStamp(ctx context.Context, sel *auth.AccountSelection) (string, error) {
	d, bound := StorageDestination(ctx)
	if bound && d.Storage != "cloud" {
		return "", fmt.Errorf("cloud RPC bound to a %s destination, which names no cloud account", d.Storage)
	}
	if !bound || d.AccountID == "" {
		id, _, err := sel.RequestAccount(ctx)
		return id, err
	}
	if err := sel.RejectionFor(d.AccountID); err != nil {
		return "", err
	}
	return d.AccountID, nil
}
