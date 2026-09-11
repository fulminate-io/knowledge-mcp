// SPDX-License-Identifier: Apache-2.0

// graphsel_canonical.go — the canonical spelling of a graph instance name, as
// the CLIENT knows it.
//
// THIS IS A SECOND COPY OF A RULE WHOSE AUTHORITY IS THE SERVER'S. The
// authoritative refusal is store.RefuseNonCanonicalGraphName in
// cmd/knowledge-server/internal/store/graph_name_canonical.go. This copy exists
// because the client CANNOT IMPORT IT: cmd/knowledge and cmd/knowledge-server
// are separate modules whose only shared contract is generated protobuf, so a
// client-side fence has no way to call the server's function.
//
// THE SLUG RULE WENT WITH THE GRAPHS IT SPELLED. This file used to carry
// SlugifyPracticeLanguage, a copy of the server's store.SlugifyLanguage, and the
// two fences that read it — RefuseNonCanonicalPracticeLanguage and
// LegacyPracticeSelector — because the LEGACY `language` selector addressed
// eight instance-keyed practice graphs and a caller writing "Design Patterns"
// had to be told about "design-patterns". Those graphs were retired and the
// selector with them: `language` now addresses no practice graph on any arm, so
// there is no non-canonical legacy spelling left to name and no legacy selector
// left to compose. Both fences and the transform went, and the server's
// SlugifyLanguage went with them. What is left is the ONE question a client
// still has to answer, which CanonicalGraphName answers: what does an
// UNSELECTED address of a singleton family resolve to.
//
// THE TWO DIVERGENCE DIRECTIONS ARE NOT SYMMETRIC, which is why the shared case
// table for the surviving rule exists. Divergence toward ACCEPTING TOO MUCH is
// caught loudly and late: the client lets a name through, and the server refuses
// the read, so the author learns at ingest instead of at authoring time.
// Divergence toward REFUSING TOO MUCH is caught by NOTHING at runtime — it
// silently blocks a name the server would have accepted, and nobody sees an
// error that says so.

package graphsel

import (
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// singletonGraphName is the ONE instance name the singleton families can be
// addressed under. Their server-side resolvers ignore the selector's name
// entirely and open this graph, so any other spelling names a graph no read of
// the family can ever reach.
const singletonGraphName = "default"

// CanonicalGraphName returns the canonical spelling of a graph instance name for
// its family.
//
// Two arms:
//
//   - GraphLinkage, GraphChecks and GraphPractice are SINGLETONS: they
//     canonicalise to "default" whatever the caller wrote, because their
//     resolvers ignore the name and open "default".
//   - Every other family keys raw at both ends and returns the name unchanged.
//
// PRACTICE MOVED INTO THE SINGLETON ARM and no family transforms a name here any
// more. The four slug transforms that used to live beside this function went
// with the eight instance-keyed practice graphs they spelled: there is one
// combined graph and it is named "default", so a caller holding any other
// practice name is naming nothing rather than naming one of eight.
//
// IT IS THE RULE THE SYNC ARMS APPLY NOW, which is the change this replaced. A
// push of practice/go once came to export practices/default.bin because the arms
// read a per-instance rule where this singleton one applied; the arms refuse the
// name outright today (sync_practice_name.go) instead of composing a selector
// for it.
//
// GraphKnowledge is DELIBERATELY NOT in the singleton arm. The server accepts a
// small list of root aliases for it (knowledgeRootNameAliases), so a client rule
// pinning it to "default" would refuse names the server allows. Its guard stays
// where it already is, in the server's store package.
func CanonicalGraphName(gt kgtypes.GraphType, name string) string {
	switch gt {
	case kgtypes.GraphLinkage, kgtypes.GraphChecks, kgtypes.GraphPractice:
		return singletonGraphName
	default:
		return name
	}
}

// IsCanonicalGraphName reports whether name is already the canonical spelling for
// its family.
//
// It is the predicate a boundary uses to REFUSE rather than to rewrite.
// Canonicalising a caller's name in place would leave them believing they wrote
// the graph they named, and would put the rename in the one place nobody reads.
func IsCanonicalGraphName(gt kgtypes.GraphType, name string) bool {
	return name == CanonicalGraphName(gt, name)
}
