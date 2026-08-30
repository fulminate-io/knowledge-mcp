// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// genre.go classifies a thought as MACHINE-genre (programmatically generated)
// versus HUMAN-genre (authored by an implementer/main/researcher agent). The
// blind-spots denominator uses this to EXCLUDE machine-genre clusters: a
// machine-generated code-smell cluster is thin on charges by construction
// (nobody charges machine observations), so counting it as an under-evidenced
// blind spot is noise, not signal.
//
// EVERY MARKER BELOW IS HISTORICAL AND FROZEN. The dream pipeline and the dream
// worker that stamped these facets have both been removed, so NO PRODUCER of any
// of them exists any more. The marker sets are kept because the thoughts they
// classify are STILL STORED: deleting a marker does not remove data, it silently
// reclassifies already-stored machine thoughts as human and corrupts the
// blind-spots denominator. Treat the three lists as a fixed record of what was
// once stamped, never as a live mirror of a producing constant.
//
// The classifier is DATA-DRIVEN over three node facets, each backed by a
// package-level marker set so the markers are data, not literals scattered through
// the switch. A thought is machine-genre if ANY facet matches.

// machineGenreSourcePrefixes are source-metadata prefixes marking a machine
// origin. "dream:" covers the legacy dream-pipeline corpus (e.g. source=
// "dream:analyze"), which predates the dream worker and is equally still stored.
// HISTORICAL, FROZEN — no producer remains. Source/origin prefixes are the
// PRIMARY signal.
var machineGenreSourcePrefixes = []string{"dream:"}

// machineGenreOriginPrefixes are origin-metadata prefixes marking a machine
// origin. "worker:" is a HISTORICAL origin stamp, FROZEN: the dream worker that
// stamped origin="worker:<name>" on every thought it produced no longer exists,
// and neither does the canonical const this list once mirrored. Thoughts
// produced before the worker system was removed still carry the stamp, so the
// list is fixed rather than kept in sync with anything. Source/origin prefixes
// are the PRIMARY signal.
var machineGenreOriginPrefixes = []string{"worker:"}

// machineGenreSessionMarkers are session-label substrings marking a machine
// session. "dream-code-smells" was the dream code-smell scan session; like the
// prefixes above it is HISTORICAL and FROZEN, retained because those sessions'
// thoughts are still stored. The session marker is a FALLBACK signal — used when
// neither the source nor origin facet carries a machine prefix but the enclosing
// session is a known machine session.
var machineGenreSessionMarkers = []string{"dream-code-smells"}

// isMachineGenreThought reports whether a thought is machine-genre, checking the
// three facets in priority order (source prefix, then origin prefix — the primary
// signals — then the enclosing-session marker fallback). enclosingSession is the
// thought's session label (empty when unknown / no session). Returns true if ANY
// facet matches.
func isMachineGenreThought(n *knowledgev1.Node, enclosingSession string) bool {
	if n == nil {
		return false
	}
	source := kgtypes.Value(n, "source")
	for _, p := range machineGenreSourcePrefixes {
		if strings.HasPrefix(source, p) {
			return true
		}
	}
	origin := kgtypes.Value(n, "origin")
	for _, p := range machineGenreOriginPrefixes {
		if strings.HasPrefix(origin, p) {
			return true
		}
	}
	for _, m := range machineGenreSessionMarkers {
		if m != "" && strings.Contains(enclosingSession, m) {
			return true
		}
	}
	return false
}

// FetchSessionLabelsByThought builds the thoughtID→enclosing-session-label map
// from ONE bulk EdgeKGContains read (NOT N per-thought lookups), reusing the
// deriveSessionSiblings group-by-session pattern: EdgeKGContains is
// session(From)→thought(To), so grouping the To endpoints by their From session
// node and hydrating the session nodes' SymbolName labels yields each thought's
// session label. Its one caller is the loop's computeBlindSpots, which feeds the
// session-marker facet of the genre classifier. A thought with no enclosing session
// is absent from the map (its lookup returns ""). Best-effort: a read error yields an
// empty map (the session-marker facet then never fires, leaving source/origin intact).
//
// src routes BOTH reads through the per-pass memo: the edge read is the SAME bulk
// EdgeKGContains read deriveSessionSiblings issues (memoKGContainsEdges), and the
// session-node hydrate comes off the resident session snapshot with a residual-only
// wire read (memoCorpusNodes). A nil/non-memo src reads the wire exactly as before.
func FetchSessionLabelsByThought(ctx context.Context, gc Caller, thoughtIDs []string, src CorpusSource) map[string]string {
	out := map[string]string{}
	if gc == nil || len(thoughtIDs) == 0 {
		return out
	}
	idSet := make(map[string]bool, len(thoughtIDs))
	for _, id := range thoughtIDs {
		idSet[id] = true
	}
	edges := memoKGContainsEdges(ctx, gc, thoughtIDs, src)
	// Group thought members by their enclosing session (the From endpoint) and
	// collect the distinct session IDs to hydrate for their labels.
	thoughtsBySession := make(map[string][]string)
	sessionIDSet := map[string]bool{}
	for i := range edges {
		e := &edges[i]
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeKGContains {
			continue
		}
		if !idSet[e.ToId] { // pollution guard: only in-scope thought members.
			continue
		}
		thoughtsBySession[e.FromId] = append(thoughtsBySession[e.FromId], e.ToId)
		sessionIDSet[e.FromId] = true
	}
	if len(sessionIDSet) == 0 {
		return out
	}
	sessionIDs := make([]string, 0, len(sessionIDSet))
	for sid := range sessionIDSet {
		sessionIDs = append(sessionIDs, sid)
	}
	sessionNodes := memoCorpusNodes(ctx, gc, sessionIDs, src)
	for sid, members := range thoughtsBySession {
		label := sid
		if n, ok := sessionNodes[sid]; ok && n.SymbolName != "" {
			label = n.SymbolName
		}
		for _, tid := range members {
			out[tid] = label
		}
	}
	return out
}
