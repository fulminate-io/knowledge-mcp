// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation_paths.go implements the escalation BFS and finding-builder.
// Split out from iam_escalation.go to keep both files under the 300-line cap.
//
// Phase 9.5 (OQ-7) extended the BFS to walk across AWS account boundaries.
// Each BFS state is a (Account, ID) tuple (visitKey). The walker tracks the
// current account so it can resolve the correct scoped reader for native
// EdgeAssumesRole edges and so cycle detection keys on the full tuple —
// preserving correctness in A→B→A trust cycles that principal-only dedup
// would incorrectly shortcut.

import (
	"context"
	"log/slog"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// visitKey is the BFS state key: (account, principal_id). The BFS uses
// this both for cycle detection and for parent-pointer reconstruction so
// cross-account paths render with accurate account transitions.
type visitKey struct {
	Account string
	ID      string
}

// escalationPath represents one privilege escalation chain from a source
// principal to an admin principal. Nodes is the ordered list of principal
// IDs traversed; the last entry is always the admin sink. Accounts is a
// parallel slice recording the account context of each node — used by
// the narrative builder and the cross-account flag computation. For
// single-account paths all entries are the same account.
type escalationPath struct {
	Source   string
	Target   string
	Nodes    []string
	Accounts []string
	Edges    []iamInferredEdge
}

// accountScopedLookup returns the scoped reader for an account name, or nil
// if that account is not loaded. Used by the BFS to query native
// EdgeAssumesRole edges against the correct per-account graph when the
// walker transitions into a new account.
type accountScopedLookup func(account string) *cloudReader

// findEscalationPaths runs BFS from each source principal looking for the
// shortest path to any admin in admins. Edges per node are the union of:
//
//   - inferred[currentID]    (rule-derived assume_role / execute_as / impersonate)
//   - native EdgeAssumesRole forward edges in the per-account scoped reader
//
// Returns one path per (source, admin target) pair. Cap depth at maxDepth
// hops. The BFS uses parent pointers to reconstruct paths after a hit.
// defaultAccount is the fallback account context for sources whose IDs
// don't carry an ARN account segment (e.g. non-ARN principal identifiers
// synthesized during fixture setup). Source IDs that ARE ARNs derive
// their starting account from the ARN.
func findEscalationPaths(
	ctx context.Context,
	scopedLookup accountScopedLookup,
	inferred map[string][]iamInferredEdge,
	admins map[string]bool,
	sources []string,
	defaultAccount string,
	maxDepth int,
) []escalationPath {
	var out []escalationPath
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return out
		}
		if admins[src] {
			continue
		}
		startAccount := accountFromARN(src)
		if startAccount == "" {
			startAccount = defaultAccount
		}
		paths := bfsToAdmin(ctx, scopedLookup, inferred, admins, src, startAccount, maxDepth)
		out = append(out, paths...)
	}
	return out
}

// parentInfo holds BFS back-pointer state for path reconstruction.
type parentInfo struct {
	Parent visitKey
	Edge   iamInferredEdge
}

// bfsToAdmin walks outgoing edges from src looking for any admin target,
// returning at most one path per admin target. Stops expanding any node
// once admin is reached at minimum depth. BFS state is keyed by
// visitKey (account, ID) so a single principal can be safely revisited
// in a different account context without being rejected as a cycle.
func bfsToAdmin(
	ctx context.Context,
	scopedLookup accountScopedLookup,
	inferred map[string][]iamInferredEdge,
	admins map[string]bool,
	src string,
	startAccount string,
	maxDepth int,
) []escalationPath {
	start := visitKey{Account: startAccount, ID: src}
	parents := map[visitKey]parentInfo{start: {}}
	depth := map[visitKey]int{start: 0}
	queue := []visitKey{start}
	hits := map[visitKey]struct{}{}

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil
		}
		current := queue[0]
		queue = queue[1:]
		curDepth := depth[current]
		if curDepth >= maxDepth {
			continue
		}
		for _, e := range outgoingEdgesFor(ctx, scopedLookup, inferred, current) {
			if e.ToID == "" || e.ToID == current.ID {
				continue
			}
			nextAcct := accountForNext(current.Account, e.ToID)
			nextKey := visitKey{Account: nextAcct, ID: e.ToID}
			if _, seen := depth[nextKey]; seen {
				slog.Debug("topology/iam_escalation: cycle skipped",
					"from", current, "to", nextKey)
				continue
			}
			depth[nextKey] = curDepth + 1
			parents[nextKey] = parentInfo{Parent: current, Edge: e}
			if admins[nextKey.ID] {
				hits[nextKey] = struct{}{}
				continue
			}
			queue = append(queue, nextKey)
		}
	}
	return reconstructPaths(start, hits, parents)
}

// accountForNext returns the account of the successor node. If the
// successor ID is an ARN carrying its own account segment, that account
// wins. Otherwise the walker inherits the current account context (used
// for self-loops and non-ARN principals like account-root markers).
func accountForNext(currentAccount, nextID string) string {
	if acct := accountFromARN(nextID); acct != "" {
		return acct
	}
	return currentAccount
}

// accountFromARN extracts the 12-digit account segment from an IAM ARN
// ("arn:aws:iam::ACCOUNT:resource"). Returns the empty string for ARNs
// without an account segment or for non-ARN principal IDs.
func accountFromARN(id string) string {
	if !strings.HasPrefix(id, "arn:aws:iam::") {
		return ""
	}
	rest := strings.TrimPrefix(id, "arn:aws:iam::")
	idx := strings.Index(rest, ":")
	if idx <= 0 {
		return ""
	}
	acct := rest[:idx]
	if acct == "aws" {
		return ""
	}
	return acct
}

// outgoingEdgesFor returns the union of inferred edges from the current
// visitKey's ID and any native EdgeAssumesRole forward edges from the
// scoped cloud graph of the current account. Native edges are converted
// to iamInferredEdge with kind=iamEdgeAssumeRole so the BFS treats them
// uniformly. The per-account scoped reader lookup lets the walker pull
// native edges from the correct graph when BFS has pivoted into a new
// account context.
func outgoingEdgesFor(
	ctx context.Context,
	scopedLookup accountScopedLookup,
	inferred map[string][]iamInferredEdge,
	current visitKey,
) []iamInferredEdge {
	out := make([]iamInferredEdge, 0, len(inferred[current.ID]))
	for _, e := range inferred[current.ID] {
		if e.ToID == e.FromID {
			continue // skip self-loops (already counted in admin set)
		}
		out = append(out, e)
	}
	if scopedLookup == nil {
		return out
	}
	scoped := scopedLookup(current.Account)
	if scoped == nil {
		return out
	}
	edges, _ := scoped.iterEdges(ctx, current.ID, outgoingEdges, []kgtypes.EdgeType{kgtypes.EdgeAssumesRole})
	for _, e := range edges {
		out = append(out, iamInferredEdge{
			FromID:  e.FromId,
			ToID:    e.ToId,
			Account: current.Account,
			Kind:    iamEdgeAssumeRole,
			Reason:  "native ASSUMES_ROLE edge from cloud graph",
		})
	}
	return out
}

// reconstructPaths walks the BFS parent map for each admin hit and returns
// the reconstructed escalation paths.
func reconstructPaths(start visitKey, hits map[visitKey]struct{}, parents map[visitKey]parentInfo) []escalationPath {
	var out []escalationPath
	for admin := range hits {
		var nodes []string
		var accts []string
		var edges []iamInferredEdge
		cur := admin
		for cur != start {
			pi, ok := parents[cur]
			if !ok || pi.Parent == (visitKey{}) {
				break
			}
			nodes = append([]string{cur.ID}, nodes...)
			accts = append([]string{cur.Account}, accts...)
			edges = append([]iamInferredEdge{pi.Edge}, edges...)
			cur = pi.Parent
		}
		nodes = append([]string{start.ID}, nodes...)
		accts = append([]string{start.Account}, accts...)
		out = append(out, escalationPath{
			Source:   start.ID,
			Target:   admin.ID,
			Nodes:    nodes,
			Accounts: accts,
			Edges:    edges,
		})
	}
	return out
}

// Finding-rendering helpers (buildEscalationFinding, collectEvidence,
// pathMinConfidence, pathHasCrossAccount) live in iam_escalation_finding.go
// to keep this file under the 300-line production cap.
