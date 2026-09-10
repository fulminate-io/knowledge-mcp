// SPDX-License-Identifier: Apache-2.0

// graphsel_canonical.go — the canonical spelling of a graph instance name, as
// the CLIENT knows it.
//
// THIS IS A SECOND COPY OF A RULE WHOSE AUTHORITY IS THE SERVER'S. The
// authoritative slug rule is store.SlugifyLanguage in
// cmd/knowledge-server/internal/store/language_detect.go, and the authoritative
// refusal is store.RefuseNonCanonicalGraphName, beside it in
// graph_name_canonical.go. This copy exists because the
// client CANNOT IMPORT EITHER: cmd/knowledge and cmd/knowledge-server are
// separate modules whose only shared contract is generated protobuf, so a
// client-side fence has no way to call the server's function.
//
// PRACTICE IS A SINGLETON NOW, so the slug rule and the graph-NAME rule are two
// different rules and this file states them separately. CanonicalGraphName puts
// practice in the singleton arm beside linkage and checks: an UNSELECTED address
// of the family is the combined graph, which is named "default".
// SlugifyPracticeLanguage keeps the four transforms, because the LEGACY
// `language` selector still addresses the eight old instance-keyed graphs and a
// caller writing "Design Patterns" still has to be told about "design-patterns".
// The server's SlugifyLanguage is unchanged and is still that rule's authority.
//
// "EVERY OTHER SPELLING NAMES A GRAPH NO READ OF THE FAMILY CAN OPEN" WOULD BE
// FALSE, and an earlier version of this header said it. The eight pre-singleton
// graphs are still on disk and still readable: the server's practice selector
// policy consumes `language`, and its resolver opens the graph that language
// names, as given. Two client callers need THAT fact rather than the singleton
// rule — the legacy read arms, and the SYNC arms, which address a legacy
// practice graph by the name the caller supplied until the migration and the
// cleanup ticket retire the selector. RefuseNonCanonicalPracticeLanguage and
// LegacyPracticeSelector below serve those callers; CanonicalGraphName answers
// the different question of what an UNSELECTED practice address resolves to.
//
// THE TWO DIVERGENCE DIRECTIONS ARE NOT SYMMETRIC, which is why the shared case
// table exists. Divergence toward ACCEPTING TOO MUCH is caught loudly and late:
// the client lets a name through, and the server refuses the read, so the
// author learns at ingest instead of at authoring time. Divergence toward
// REFUSING TOO MUCH is caught by NOTHING at runtime — it silently blocks a name
// the server would have accepted, and nobody sees an error that says so. The
// shared vector at testdata/practice_graph_name_canonical_cases.json, read by a
// test in each module, is what catches the second direction before it ships; it
// now pins SlugifyPracticeLanguage against store.SlugifyLanguage rather than
// CanonicalGraphName, which no longer applies those transforms to any family.

package graphsel

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
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
// more. The four slug transforms did not disappear — they are
// SlugifyPracticeLanguage below, which the legacy `language` selector uses to
// reach one of the eight old graphs. What changed is that they no longer answer
// "what is this family's canonical graph name", because the COMBINED practice
// graph has exactly one and it is "default".
//
// IT IS NOT THE RULE THE SYNC ARMS APPLY, and reading it as one is how a push of
// practice/go came to export practices/default.bin. A caller holding a legacy
// practice name is not asking what the combined graph is called; it is naming one
// of the eight, and the pair below is what serves it.
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

// SlugifyPracticeLanguage returns the canonical spelling of a LEGACY practice
// language: lowercase, "/"→"-", " "→"-", "+"→"plus". It is the client's copy of
// store.SlugifyLanguage and the subject of the shared vector at
// testdata/practice_graph_name_canonical_cases.json.
//
// IT IS NOT THE COMBINED GRAPH'S NAME RULE. That graph is named "default" and
// CanonicalGraphName says so; this transform survives for the callers that hold a
// LEGACY practice name — the `language` read selector and the sync arms, which
// address the eight pre-singleton graphs by their slug, so a caller passing the
// display spelling "JavaScript/TypeScript" is answered about
// "javascript-typescript". A practice WRITE never reaches it: `language` is
// refused on every practice write arm.
func SlugifyPracticeLanguage(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "+", "plus")
	return s
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

// RefuseNonCanonicalPracticeLanguage reports why a LEGACY practice name may not
// be addressed, and nil when it may. op names the operation for the message
// ("sync push", "sync pull").
//
// IT REFUSES RATHER THAN SLUGIFIES, which is the whole reason it is a fence and
// not a transform. Rewriting "Design Patterns" to "design-patterns" here would
// leave the caller believing they addressed the graph they typed while the bytes
// went somewhere else, and it would put the rename in the one place nobody reads.
// The message therefore names both spellings, exactly as the server's
// store.RefuseNonCanonicalGraphName does for the create channels.
//
// THE RULE IS THE PER-INSTANCE ONE, not the singleton one: a legacy name is
// canonical when it already equals its slug. The singleton rule ("the name must
// be default") is the rule for CREATING a practice graph, and these eight already
// exist — a caller moving one of them between machines is not creating anything.
// An EMPTY name never reaches here: that is the combined graph, which the arms
// address by sending no legacy selector at all.
func RefuseNonCanonicalPracticeLanguage(op, language string) error {
	if language == "" {
		return nil
	}
	canonical := SlugifyPracticeLanguage(language)
	if language == canonical {
		return nil
	}
	return fmt.Errorf(
		"%s: practice graph name %q is not canonical - the canonical spelling is %q, and that is what the graph is stored and addressed under; %q names a graph no read of the family can reach",
		op, language, canonical, language)
}

// LegacyPracticeSelector builds the wire selector addressing ONE of the eight
// pre-singleton practice graphs.
//
// THE NAME RIDES `language` AND NEVER `name`, and that is the server's contract
// rather than a preference: the practice selector policy consumes `language` and
// carries no instance field, so a set `name` is REFUSED by the selector
// validation before any routing happens, while a set `language` is passed to the
// resolver and used as given. GraphSelectorFor cannot express this — practice is
// in its FieldNone arm, which is correct for every caller that means the combined
// graph and wrong for the one caller that means a legacy one.
//
// It is deliberately not reachable with an empty language: an unselected practice
// address is GraphSelectorFor's business, and composing an empty legacy selector
// here would put two spellings of "the combined graph" on the wire.
func LegacyPracticeSelector(language string) *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{
		Graph:    string(kgtypes.GraphPractice),
		Family:   FamilyOf(kgtypes.GraphPractice),
		Language: language,
	}
}
