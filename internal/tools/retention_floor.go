// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"log/slog"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// retention_floor.go — the position this CLIENT reports to the server, taken
// across every consumer of the erase feed it runs.
//
// WHY THE CLIENT TAKES THE MINIMUM. The floor this client reports is what the
// server's segment_rebuild completeness refusal is measured against: the server
// compares it with store.ErasureReapFloor and refuses the scan outright when the
// window it would serve may be missing erasures the reap has already destroyed. This
// process runs TWO consumers of the same erase feed with INDEPENDENT durable
// positions — the rebuild drain and the delta merge — and both scan the same axis
// for the same graph. If each reported its own position, the AHEAD one would
// raise the server's watermark past an erasure the LAGGING one has not read, and
// the reap behind that watermark would destroy it. The lagging consumer then
// never learns the id was deleted: the document stays in its shipped corpus
// forever, with nothing left able to name it.
//
// TAKING THE FLOOR HERE IS SUFFICIENT, and the reason is compositional rather
// than incidental: once every arrival carries the same minimum, the server's
// max-over-arrivals lands exactly on that minimum — the true floor — instead of
// on whichever consumer happened to scan last.
//
// A TRAILING PERSISTED VALUE IS THE SAFE DIRECTION, so this reads the DURABLE
// merge position and deliberately does NOT substitute a fresher in-process one to
// tighten the floor. A floor below the truth reaps LESS than it could; a floor
// above it reaps an unread erasure. Only the second is unrecoverable.

// retentionFloorFor returns the position to report for one graph: the MINIMUM of
// this consumer's own durable position and the other consumer's.
//
// A PEER WITH NO POSITION IMPOSES NO FLOOR — the minimum is taken over the
// consumers that HAVE one. The obvious alternative, a minimum including zero, is
// retention-correct and scope-catastrophic: on the arms that send no scan bound of
// their own the same field is still the scan's lower bound, so a peer at zero would
// drag their read down to a whole-corpus read.
//
// AND EXCLUDING IT IS SAFE, for a different reason on each side:
//   - A DELTA consumer with no position PULLS NOTHING. Its horizon resolution
//     reports "no position" rather than zero, and the caller treats that as
//     nothing to pull — it does not scan from zero, it does not scan. So it has
//     ingested nothing that a reaped erasure could have left stranded, and when it
//     does acquire a position that position is either its own post-drain one or
//     the rebuild position, both of which are already at or above this floor.
//   - A REBUILD arm at zero does scan, but reads the LIVE set. A row erased before
//     the reap is already gone from it, so there is nothing for a missing erasure
//     record to fail to remove.
//
// The window this would otherwise open — a peer that has a position but has not
// scanned — does not exist, because positions are written BY completed drains.
//
// AN UNREADABLE POSITION IS NOT THE SAME AS AN ABSENT ONE and yields no floor at
// all. Falling through to the readable side would report a floor nobody has
// verified the other consumer reached, which is the precise failure this helper
// exists to prevent.
// A CALLER SENDING ZERO KEEPS SENDING ZERO. The reset path and the repair arm
// hold no durable position, so they impose no floor and must not acquire one
// here: routing them through this helper and letting it substitute a persisted
// value would put a non-zero bound on a scan whose whole contract is to read from
// the beginning.
//
// IT TAKES own AND MINS IT WITH BOTH PERSISTED POSITIONS rather than comparing
// own against a single peer. Mining all three is what makes the same helper
// correct at both sites: the rebuild arm's own value IS its persisted one, so
// including it changes nothing there, while the delta arm's own value is the
// horizon its caller is at, which no persisted read reproduces. An earlier form
// that compared own against ONE peer read the delta arm's own position back as
// its peer and returned it unchanged — the exact defect this helper exists to
// prevent.
func retentionFloorFor(shipper SegmentShipper, gt kgtypes.GraphType, name string, own int64) int64 {
	if shipper == nil || own <= 0 {
		return 0
	}
	rebuildPos, _, rerr := shipper.LoadRebuildState(gt, name)
	if rerr != nil {
		slog.Warn("retention floor: the rebuild position is unreadable — reporting no position rather than one "+
			"this client cannot vouch for across both of its consumers",
			"graph_type", gt, "name", name, "err", rerr)
		return 0
	}
	mergePos, merr := shipper.LoadMergeWatermark(gt, name)
	if merr != nil {
		slog.Warn("retention floor: the delta-merge position is unreadable — reporting no position rather than "+
			"one this client cannot vouch for across both of its consumers",
			"graph_type", gt, "name", name, "err", merr)
		return 0
	}
	floor := own
	for _, pos := range []int64{rebuildPos, mergePos} {
		if pos > 0 && pos < floor {
			floor = pos
		}
	}
	return floor
}
