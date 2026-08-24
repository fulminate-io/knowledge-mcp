// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// manage_status_coverage_erasure.go — the deletion-backlog cell: the SERVER's
// count of journal rows awaiting a consumer, beside THIS CLIENT's ages for the
// two consumers that drain them.
//
// SPLIT FROM manage_status_coverage.go FOR THE LINE BUDGET, and the seam is the
// honest one: everything here concerns the erasure backlog and nothing else
// reads it. The row type, its constructor and the main formatter stay in the
// sibling.
//
// THE TWO HALVES ANSWER DIFFERENT QUESTIONS. The server's counters say how much
// is piling up — a fact this client cannot derive. The local ages say whether
// either consumer is still moving, which is the case an arrival-driven stall
// alarm structurally cannot report, since a consumer that never arrives never
// triggers an evaluation. Rendered adjacently so the pair reads as one story.

// consumerAgesFor reports how long since each of this client's two erase-feed
// consumers last advanced, or ZERO for one that has never recorded a position.
//
// ZERO IS "NEVER", AND THAT DISTINCTION IS THE POINT. A consumer with no position
// has not started — which is not the same as being stalled — so the renderer must
// print "never" rather than an age. Measuring from a zero position instead yields
// the age of the unix epoch, the same inversion that once reported a
// sub-millisecond-old erasure as decades behind.
//
// An unreadable record also reports zero: an age nobody can vouch for is worse
// than an honest "never", and this column must not fail a status table.
func consumerAgesFor(deps ClientDeps, gt kgtypes.GraphType, name string, now int64) (rebuildAge, mergeAge int64) {
	sr := deps.SegmentCoverage()
	if sr == nil {
		return 0, 0
	}
	ageOf := func(pos int64, err error) int64 {
		if err != nil || pos <= 0 || now <= pos {
			return 0
		}
		return now - pos
	}
	rebuildPos, _, rerr := sr.LoadRebuildState(gt, name)
	mergePos, merr := sr.LoadMergeWatermark(gt, name)
	return ageOf(rebuildPos, rerr), ageOf(mergePos, merr)
}

// erasureAgeUnknown is the wire value of newest_erasure_age_nanos meaning the
// server COULD NOT READ the journal, so the backlog is unknown rather than empty
// and the count arriving beside it is not meaningful.
//
// IT IS NAMED HERE RATHER THAN IMPORTED. Generated protobuf is the only contract
// the two sides share, so a sentinel VALUE is declared on each side against the
// field's documented meaning — there is no shared package to hold it.
//
// COMPARED AS "AT OR BELOW", not equality: an age cannot be negative by
// construction, so every negative value means the same thing and there is no
// other reading to lose.
const erasureAgeUnknown int64 = -1

// erasureBacklogCell renders the deletion backlog beside the ages of the two
// consumers that drain it, so the pair reads as ONE story: how much is waiting,
// and whether anything is still collecting it.
//
// NO BACKLOG RENDERS AS "none", NOT AS A BLANK. A blank cell where a zero belongs
// is how an operator learns to ignore a column.
//
// UNKNOWN RENDERS AS "unknown", VISIBLY DIFFERENT FROM "none". The server failed
// to read the journal, which is not the same as there being nothing in it, and
// collapsing the two would report certainty nobody has. The count is NOT rendered
// in that state: a number here would read as a backlog the server never measured.
//
// A CONSUMER WITH NO POSITION RENDERS "never", NOT AN AGE. It has not started,
// which is not the same as being stalled — and an age measured from a zero
// position would be the age of the unix epoch, saying the opposite of the truth.
func erasureBacklogCell(r CoverageRow) string {
	backlog := "none"
	switch {
	case r.NewestErasureAgeNanos <= erasureAgeUnknown:
		backlog = "unknown"
	case r.RetainedErasures > 0:
		backlog = fmt.Sprintf("%d · newest %s", r.RetainedErasures,
			time.Duration(r.NewestErasureAgeNanos).Round(time.Minute))
	}
	return fmt.Sprintf("%s (rebuild %s, merge %s)",
		backlog, consumerAgeTerm(r.RebuildPosAgeNanos), consumerAgeTerm(r.MergePosAgeNanos))
}

// consumerAgeTerm renders one consumer's position age, or "never" when it holds
// no position at all.
func consumerAgeTerm(ageNanos int64) string {
	if ageNanos <= 0 {
		return "never"
	}
	return time.Duration(ageNanos).Round(time.Minute).String()
}
