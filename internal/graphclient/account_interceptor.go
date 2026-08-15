// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"

	"connectrpc.com/connect"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// newAccountInterceptor returns the outbound interceptor that stamps the
// selected Fulminate account on every cloud RPC, and refuses a selection the
// gateway has already been observed to reject.
//
// It is the Connect-side half of the two stamping chokepoints; the raw
// /v1/sync and /v1/segments surfaces are stamped inside
// auth.Transport.issueBytes. Both read the same auth.AccountSelection, so
// stamping and refusing cannot disagree between them.
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
func newAccountInterceptor(sel *auth.AccountSelection) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			id, err := sel.IDForRequest(ctx)
			if err != nil {
				return nil, connect.NewError(connect.CodePermissionDenied, err)
			}
			if id != "" {
				req.Header().Set(auth.AccountHeaderName, id)
			}
			return next(ctx, req)
		}
	})
}
