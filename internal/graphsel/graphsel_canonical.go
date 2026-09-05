// SPDX-License-Identifier: Apache-2.0

// graphsel_canonical.go — the canonical spelling of a graph instance name, as
// the CLIENT knows it.
//
// THIS IS A SECOND COPY OF A RULE WHOSE AUTHORITY IS THE SERVER'S. The
// authoritative practice rule is store.SlugifyLanguage in
// cmd/knowledge-server/internal/store/language_detect.go, and the authoritative
// refusal is store.RefuseNonCanonicalGraphName, beside it in
// graph_name_canonical.go. This copy exists because the
// client CANNOT IMPORT EITHER: cmd/knowledge and cmd/knowledge-server are
// separate modules whose only shared contract is generated protobuf, so a
// client-side fence has no way to call the server's function.
//
// THE TWO DIVERGENCE DIRECTIONS ARE NOT SYMMETRIC, which is why the shared case
// table exists. Divergence toward ACCEPTING TOO MUCH is caught loudly and late:
// the client lets a name through, and the server refuses the create, so the
// author learns at ingest instead of at authoring time. Divergence toward
// REFUSING TOO MUCH is caught by NOTHING at runtime — it silently blocks a name
// the server would have accepted, and nobody sees an error that says so. The
// shared vector at testdata/practice_graph_name_canonical_cases.json, read by a
// test in each module, is what catches the second direction before it ships.

package graphsel

import (
	"strings"

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
// Three arms:
//
//   - GraphLinkage and GraphChecks are SINGLETONS: they canonicalise to
//     "default" whatever the caller wrote, because their resolvers ignore the
//     name and open "default".
//   - GraphPractice applies the same four transforms store.SlugifyLanguage
//     applies, in the same order.
//   - Every other family keys raw at both ends and returns the name unchanged.
//
// GraphKnowledge is DELIBERATELY NOT in the singleton arm. The server accepts a
// small list of root aliases for it (knowledgeRootNameAliases), so a client rule
// pinning it to "default" would refuse names the server allows. Its guard stays
// where it already is, in the server's store package.
func CanonicalGraphName(gt kgtypes.GraphType, name string) string {
	switch gt {
	case kgtypes.GraphLinkage, kgtypes.GraphChecks:
		return singletonGraphName
	case kgtypes.GraphPractice:
		s := strings.ToLower(name)
		s = strings.ReplaceAll(s, "/", "-")
		s = strings.ReplaceAll(s, " ", "-")
		s = strings.ReplaceAll(s, "+", "plus")
		return s
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
