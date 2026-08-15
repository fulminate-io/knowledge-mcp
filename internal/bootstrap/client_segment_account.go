// SPDX-License-Identifier: Apache-2.0

// client_segment_account.go — the account dimension of the segment L2 cache
// root. It lives beside client_segment.go rather than inside it because that
// file is already close to the length cap.

package bootstrap

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// accountSegmentRoot roots the segment L2 cache under an account-specific
// directory when a Fulminate account is selected, and under today's unchanged
// path when none is.
//
// This is a STRUCTURAL boundary, not a runtime guard: once two accounts' blobs
// cannot share a directory, serving account A's cached segments to a session
// the user has told the client is account B is impossible on disk and across
// daemon restarts, with no correctness resting on any invalidation firing.
//
// The unset path is byte-identical to before this feature, so an existing
// single-account user's warm cache is not orphaned by upgrading. Switching back
// to a previous account finds that account's cache still warm under its own
// path — nothing is deleted here.
//
// The id becomes a path element, so it is sanitized with the same replacer
// segmentdist.graphCacheDirFor uses on graph names.
func accountSegmentRoot(root, accountID string) string {
	base := filepath.Join(root, "segments")
	if accountID == "" {
		return base
	}
	safe := strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(accountID)
	if safe == "" {
		safe = "_"
	}
	return filepath.Join(base, "account-"+safe)
}

// selectedAccountForSegments reads the selected account once, at segment
// manager construction. The manager caches its cacheDir for its lifetime, so a
// mid-session switch is handled by the daemon restart the dispatcher performs,
// with the manager's own fail-closed check as the backstop.
func selectedAccountForSegments() string {
	return auth.SelectedAccount().ID(context.Background())
}
