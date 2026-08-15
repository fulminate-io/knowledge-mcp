// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// SelectedAccountID reports the Fulminate account this machine's cloud calls
// are routed to, or "" when no selection is stored.
//
// It sits beside LoggedIn on the Router because the Router is already the one
// place the backend identity is answered — the pipeline compares BOTH values
// to decide whether the identity moved, and an account switch is cloud->cloud,
// so it never shows up in the login boolean.
//
// The read is TTL-cached in auth.AccountSelection, so this is safe to call on
// the per-tool-call flip-check path.
//
// It lives in its own file rather than in router.go deliberately: a landed
// gate caps router.go's length, and that file is already over the cap.
func (r *Router) SelectedAccountID(ctx context.Context) string {
	return auth.SelectedAccount().ID(ctx)
}
