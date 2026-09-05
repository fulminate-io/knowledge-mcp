// SPDX-License-Identifier: Apache-2.0

// sync_list.go — client-side `sync(operation:"list")` handler. Prints a table
// of sync-eligible LOCAL graphs (always) joined against the user's CLOUD
// account (when logged in), so the operator can see which graphs are synced and
// when. Eligibility = the builtin kgtypes.SyncEligible set, which is now a rule
// in its own right rather than the !SkipsLLMProcessing complement it used to
// mirror — raw web/pdf graphs are LLM-processed (they embed) and still never
// sync, so the two no longer move together — PLUS every registered custom graph type whose
// GraphTypeDef behavior declares syncable=true; the "Last synced" column comes
// from the CLOUD GraphInfo.SyncTime ONLY — never the local one (local SyncTime is
// code-collect time, a different meaning).

package tools

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// syncListRow is one rendered table row: a single sync-eligible LOCAL graph,
// annotated with whether it exists in the cloud account and the cloud-side
// last-synced time. SyncTime is the CLOUD GraphInfo.SyncTime (unix nanos),
// 0 when the graph is absent from cloud or the user is not logged in.
type syncListRow struct {
	graphType kgtypes.GraphType
	name      string
	synced    bool
	syncTime  int64
}

// handleSyncList enumerates every sync-eligible LOCAL graph and, when the user
// is logged in to Fulminate Cloud, joins it against the CLOUD catalog by
// (graphType, name) to show sync status + last-synced time. Local enumeration
// always runs; cloud enumeration is gated on the cloudStatusInfo login check.
// The authoritative "last synced" source is the CLOUD GraphInfo.SyncTime.
func handleSyncList(ctx context.Context, deps ClientDeps) kgtools.ToolResult {
	local := deps.LocalGraphCaller()
	if local == nil {
		return errorResult("sync list: local graph caller unavailable — no local server is wired (cloud-first user without `knowledge install`)")
	}

	// Login gate: mirror handleServerStatus (manage.go:205-208). When logged in
	// we additionally enumerate the cloud catalog to populate Synced?/Last synced.
	loggedIn := false
	if csi, ok := deps.(cloudStatusInfo); ok {
		loggedIn, _ = csi.CloudStatusInfo()
	}

	// Build the cloud lookup once (when logged in): (graphType, name) → cloud
	// GraphInfo carrying the authoritative SyncTime. fetchGraphNamesOfType is
	// ONE Execute per type, no fan-out.
	type cloudKey struct {
		gt   kgtypes.GraphType
		name string
	}
	// The graph types to enumerate: the builtin sync-eligible set (UNCHANGED)
	// plus every REGISTERED custom type whose behavior cascade declares
	// syncable=true. A nil GraphTypeCRUD (degraded client) yields no custom types,
	// so builtins still list. List is ONE wire call.
	syncTypes := kgtypes.SyncEligibleGraphTypes()
	syncTypes = append(syncTypes, syncableCustomTypes(ctx, deps)...)

	cloudByKey := map[cloudKey]int64{}
	if loggedIn {
		cloud := deps.GraphCaller()
		for _, gt := range syncTypes {
			infos, err := fetchGraphNamesOfType(ctx, cloud, string(gt))
			if err != nil {
				return errorResult(fmt.Sprintf("sync list: enumerate cloud %s graphs: %v", gt, err))
			}
			for _, gi := range infos {
				cloudByKey[cloudKey{gt: gt, name: gi.GetName()}] = gi.GetSyncTime()
			}
		}
	}

	var rows []syncListRow
	for _, gt := range syncTypes {
		infos, err := fetchGraphNamesOfType(ctx, local, string(gt))
		if err != nil {
			return errorResult(fmt.Sprintf("sync list: enumerate local %s graphs: %v", gt, err))
		}
		for _, gi := range infos {
			name := gi.GetName()
			st, synced := cloudByKey[cloudKey{gt: gt, name: name}]
			rows = append(rows, syncListRow{
				graphType: gt,
				name:      name,
				synced:    synced,
				syncTime:  st, // CLOUD SyncTime only; 0 when unsynced / not-logged-in
			})
		}
	}

	return textResult(renderSyncListTable(rows, loggedIn))
}

// syncableCustomTypes returns every REGISTERED custom graph type whose behavior
// cascade declares syncable=true (GetBehavior().GetSyncable()). It is the sync
// counterpart of kgtypes.SyncEligibleGraphTypes() for custom types: a custom type
// is sync-eligible iff its GraphTypeDef opts in. A nil GraphTypeCRUD (degraded
// client) or a List error yields no custom types — builtins still list. Builtins
// never appear here (they carry no GraphTypeDef record).
func syncableCustomTypes(ctx context.Context, deps ClientDeps) []kgtypes.GraphType {
	crud := deps.GraphTypeCRUD()
	if crud == nil {
		return nil
	}
	defs, err := crud.List(ctx)
	if err != nil {
		return nil
	}
	var out []kgtypes.GraphType
	for _, d := range defs {
		if d.GetBehavior().GetSyncable() {
			out = append(out, kgtypes.GraphType(d.GetName()))
		}
	}
	return out
}

// renderSyncListTable renders the locked 4-column table via stdlib text/tabwriter:
//
//	| Graph | Sync params | Synced? | Last synced |
//
// Last-synced display rule: the server reports a synced graph's last-synced
// time, falling back to its first-seen time when it was synced before
// timestamps were recorded — so a synced row ALWAYS carries SyncTime>0.
// SyncTime>0 → human time; else (unsynced, or not-logged-in) → blank. There is
// deliberately NO "synced but no time" branch.
func renderSyncListTable(rows []syncListRow, loggedIn bool) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Graph\tSync params\tSynced?\tLast synced")
	for _, r := range rows {
		synced := "no"
		switch {
		case !loggedIn:
			synced = "login required"
		case r.synced:
			synced = "yes"
		}
		last := ""
		if r.syncTime > 0 {
			last = relativeAge(time.Unix(0, r.syncTime))
		}
		fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\n",
			r.graphType, r.name, syncParamsDisplay(r.graphType, r.name), synced, last)
	}
	_ = tw.Flush()
	return b.String()
}

// syncParamsDisplay returns the (graph,name) selector label a user passes to
// `sync push` for this graph, routed per-type so the right selector shows.
// DISPLAY-only — mirrors the field-routing of manageGraphSelector
// (intercept_manage_index.go:52) but emits a label string, not a GraphSelector.
func syncParamsDisplay(gt kgtypes.GraphType, name string) string {
	switch gt {
	case kgtypes.GraphPractice:
		return fmt.Sprintf("graph:practice name:%s", name)
	case kgtypes.GraphCode:
		return fmt.Sprintf("graph:code name:%s", name)
	case kgtypes.GraphCloud, kgtypes.GraphCICD:
		return fmt.Sprintf("graph:%s name:%s", gt, name)
	default:
		return fmt.Sprintf("graph:%s name:%s", gt, name)
	}
}
