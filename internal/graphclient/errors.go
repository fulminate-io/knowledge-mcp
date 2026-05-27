// SPDX-License-Identifier: Apache-2.0

package graphclient

import "errors"

// ErrNotFound is the client-local not-found sentinel. It NEVER leaves the
// client: it is what the client maps the wire's connect.CodeNotFound back to,
// and what client packages (tools/thought/workercrud) match on via
// errors.Is. graphclient is the correct home because it is the lowest-level
// client wire-leaf (it imports only gen/knowledge/v1 + connect) and is already
// imported by every consumer package. Mirrors the one-line pkg/store.ErrNotFound
// it supersedes on the client side.
//
// NOTE: distinct from cmd/knowledge/internal/auth.ErrNotFound, which is a
// separate keychain-storage sentinel and is intentionally left untouched.
var ErrNotFound = errors.New("not found")
