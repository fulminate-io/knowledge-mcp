// SPDX-License-Identifier: Apache-2.0

package hivemonitor

import (
	"context"
	"log/slog"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/paging"
)

// heartbeatLiveMembers refreshes last_seen for every live member this daemon
// hosts — the machine-health heartbeat, decoupled from claims. last_seen must
// track MACHINE liveness, not WORK liveness (decision: heartbeat = machine
// health): a registered member that is ALIVE but holds no claim must still
// refresh last_seen, or a peer reaper would falsely evict it past the
// machine-down threshold.
//
// "Live" means the session's on-disk transcript still resolves. A session whose
// transcript no longer resolves (the file is gone — the process is dead) is
// SKIPPED: that stale last_seen is exactly the machine-down signal the reaper
// detects, and must not be refreshed. The transcript readers classify a missing
// file as StateDead, and ResolveTranscript stats the file, so a gone process
// surfaces here as an unresolved handle — the dead-machine case is the
// unresolved case (a resolved handle always names a present file, i.e. an alive
// process, whether working or idle).
//
// For each live member it looks up the hives that member belongs to and fires
// one HIVE_OP_RENEW per (harness, hive). RenewHiveLease bumps the member's
// last_seen as its first UPDATE and no-ops the work-UPDATE for an idle member,
// so a RENEW IS the heartbeat — no new cloud op is needed. Renews are deduped on
// (harness, hive) within the tick. Read failures are logged and the member
// skipped — one unreachable member must not abort the sweep.
func (m *Monitor) heartbeatLiveMembers(ctx context.Context, snapByID map[string]SessionSnapshot) {
	if m.hive == nil {
		return
	}
	done := map[string]bool{} // dedupe key: harness + "\x00" + hive
	for _, snap := range snapByID {
		handle, err := ResolveTranscript(ctx, snap)
		if err != nil {
			slog.Warn("hivemonitor: heartbeat transcript resolve error", "session", snap.ID, "error", err)
			continue
		}
		if !handle.Resolved() {
			// Transcript gone → process dead → do NOT heartbeat (preserve the
			// machine-down signal for the reaper).
			continue
		}
		for _, hive := range m.memberHivesFor(ctx, handle.HarnessSessionID) {
			key := handle.HarnessSessionID + "\x00" + hive
			if done[key] {
				continue
			}
			done[key] = true
			m.renew(ctx, handle.HarnessSessionID, hive)
		}
	}
}

// memberHivesFor returns the hive ids this harness session is a registered
// member of, by querying hive_member nodes whose `session` metadata equals the
// harness id. Mirrors banEvictedMembers' Execute+QueryPlan shape, swapping the
// status='evicted' selection for a metadata.session==harness predicate. Read
// failures are logged and yield no hives (the member is skipped this tick).
//
// The membership set is consumed COMPLETE — every hive this session belongs to
// gets a renew — so the raw plan is DRAINED in keyset pages rather than taken in
// one bounded read. Nothing caps how many hives a session joins, and a member
// that fell off the read boundary would silently stop being heartbeated.
func (m *Monitor) memberHivesFor(ctx context.Context, harnessSessionID string) []string {
	if harnessSessionID == "" {
		return nil
	}
	nodes, err := paging.DrainKeysetPages(func(afterID string) ([]*knowledgev1.Node, error) {
		cursor := afterID
		resp, rerr := m.hive.Execute(ctx, &knowledgev1.ExecuteRequest{
			Target: &knowledgev1.GraphSelector{Graph: "knowledge"},
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{
					NodeTypes: []string{"hive_member"},
					MetadataPredicates: []*knowledgev1.MetadataPredicate{
						{Key: "session", Op: knowledgev1.MetadataPredicate_OP_EQ, Value: harnessSessionID},
					},
				},
				Limit: int32(paging.BrowsePageSize),
				// SET on every page including the first, where the value is empty:
				// presence is what selects the keyset browse.
				AfterId:   &cursor,
				SkipTotal: true,
			}},
		})
		if rerr != nil {
			return nil, rerr
		}
		return resp.GetNodes(), nil
	}, paging.BrowsePageSize)
	if err != nil {
		slog.Warn("hivemonitor: read member hives failed", "member", harnessSessionID, "error", err)
		return nil
	}
	var hives []string
	for _, n := range nodes {
		if h := n.GetMetadata()["hive"]; h != "" {
			hives = append(hives, h)
		}
	}
	return hives
}
