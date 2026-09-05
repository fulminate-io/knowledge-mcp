// SPDX-License-Identifier: Apache-2.0

package tools

// working_set_removal.go holds the WRITE half of the working-set seam: the
// optional deps capability a completed drop uses to stop this client wanting a
// graph it has just removed.
//
// IT LIVES IN ITS OWN FILE RATHER THAN BESIDE ITS READ TWIN, and the reason is
// measured rather than stylistic: manage_status_coverage_collect.go, which
// declares workingSetReader, is 497 lines against the repo's 500-line commit cap,
// so adding this seam and its helper there produces a file the commit gate
// refuses.
//
// IT IS TYPE-ASSERTED RATHER THAN ADDED TO ClientDeps, for exactly the reason the
// read seam beside it gives: a required ClientDeps method would have to be
// implemented by every fake that already implements SegmentCoverage — twenty-five
// of them, none of which runs a working set. The safe failure differs from the
// read side's though, and it is worth naming. An unwired READER answers "not a
// member", a claim; an unwired REMOVER removes nothing, which is a no-op on a
// client that had no membership to forget in the first place.

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// workingSetRemover is the optional deps capability a drop writes through. The
// bootstrap *client implements it (client_workingset.go).
type workingSetRemover interface {
	RemoveFromWorkingSet(gt kgtypes.GraphType, name string) bool
}

// removeFromWorkingSetFor forgets (gt, name) through the optional seam and
// reports whether a membership was actually removed.
//
// A deps that does not implement the seam reports false, which is the honest
// answer for a client that maintains no working set: there was nothing to forget.
// Callers use the boolean for logging and for tests, never to decide whether the
// drop succeeded — the drop is the server-side Execute, and this is local
// bookkeeping that follows it.
func removeFromWorkingSetFor(deps ClientDeps, gt kgtypes.GraphType, name string) bool {
	r, ok := deps.(workingSetRemover)
	if !ok {
		return false
	}
	return r.RemoveFromWorkingSet(gt, name)
}
