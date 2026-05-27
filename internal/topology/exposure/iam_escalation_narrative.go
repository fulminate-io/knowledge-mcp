// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_escalation_narrative.go builds the PMapper-style sentence-per-hop
// Summary text for an escalation finding.
//
// Phase 9 Step 2: the Summary used to be a multi-line log-style block:
//
//	Principal alice can escalate to admin in 2 hops:
//	  1. alice --[impersonate]--> bob — principal can create a console login profile...
//	  2. bob --[attach_policy]--> bob — principal can attach managed policies...
//
// PMapper's output is a single paragraph of connected sentences that reads
// like a penetration tester's description of the attack chain:
//
//	"alice can create a login profile for bob, then bob can attach an
//	admin-equivalent policy to themselves, reaching AdministratorAccess."
//
// buildPMapperNarrative produces exactly that: one sentence per edge using
// humanKindLabel as the verb phrase, separated by " then " clauses, and
// terminating in a sentence that names the final admin state.
//
// This file is intentionally split from iam_escalation_paths.go so the
// paths file stays under the 300-line soft cap even as the narrative
// logic grows (cross-account annotation, future PMapper phrasing, etc.).

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// terminalAdminState is the phrase used at the end of every narrative to
// name the admin endpoint. Matches PMapper's wording — the reference
// implementation uses "AdministratorAccess" as the conventional label for
// the effective-admin terminal regardless of how the caller reached it
// (wildcard inline policy, AdministratorAccess managed policy attachment,
// attach-policy self-promotion, etc.).
const terminalAdminState = "AdministratorAccess"

// buildPMapperNarrative returns a human-readable sentence-per-hop narrative
// for an escalation path. Returns "" only for an empty path (no edges).
//
// Format: one sentence per edge joined by ", then ", followed by a final
// sentence naming the terminal admin state. Node IDs are resolved to
// SymbolNames via resolveNodeName so the narrative reads in terms of
// principal names ("alice", "dev-user") rather than ARNs. The req
// parameter carries the wire caller and account name for name resolution —
// when req.Caller is nil (isolated unit tests) the raw node ID is used
// directly.
func buildPMapperNarrative(req Request, p escalationPath) string {
	if len(p.Edges) == 0 {
		return ""
	}
	var b strings.Builder
	for i, e := range p.Edges {
		fromName := resolvePathNodeName(req, p.Nodes[i])
		toName := resolvePathNodeName(req, p.Nodes[i+1])
		clause := fmt.Sprintf("%s %s %s", fromName, humanKindLabel(e.Kind), toName)
		if i == 0 {
			b.WriteString(clause)
		} else {
			b.WriteString(", then ")
			b.WriteString(clause)
		}
	}
	b.WriteString(", reaching ")
	b.WriteString(terminalAdminState)
	b.WriteString(".")
	return b.String()
}

// resolvePathNodeName is the name-resolution shim for narrative rendering.
// It defers to resolveNodeName when the request carries a wire caller and
// account, and falls back to the raw node ID when either is missing (keeps
// standalone unit tests — e.g. TestBuildPMapperNarrative — self-contained
// without a full cloud fixture). Empty node IDs short-circuit to "<unknown>"
// so the narrative never collapses to a dangling verb phrase.
func resolvePathNodeName(req Request, nodeID string) string {
	if nodeID == "" {
		return "<unknown>"
	}
	if req.Caller == nil || req.Name == "" {
		return nodeID
	}
	return resolveNodeName(context.Background(), req.Caller, kgtypes.GraphCloud, req.Name, nodeID)
}
