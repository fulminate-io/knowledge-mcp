// SPDX-License-Identifier: Apache-2.0

package tools

// practice_selector_messages.go holds the four practice-selector refusal
// wordings. They live in a sibling file rather than beside the code that emits
// them because their two homes — intercept_query_practice_linkage.go and
// query_arm_registry_graphs.go — are both within a handful of lines of the repo's
// 500-line file convention, which is the split rule query_arm_registry.go's own
// header states.
//
// ALL FOUR FOLLOW ONE SHAPE, the same one rankedSearchRetiredResult
// (intercept_search_reducible_graph.go) uses: say what the graph IS, say what is
// not on offer, and NAME THE CALL THAT WORKS. The generic accounting tail — "drop it or
// issue a separate call that does" — is true and useless here, because the caller
// does not know that supplying `language` IS the separate call.

// practiceListGraphsUnrouted is the rejection explanation for every browse-shaped
// param on the list-graphs arm, on the justifyRulesKnowledgeOnly precedent: the
// generic tail is replaced wherever a specific working call exists.
//
// practiceListGraphsUnrouted is the refusal for a browse filter on the LEGACY
// enumeration.
//
// THE ENUMERATION IS NO LONGER WHAT AN EMPTY SELECTOR MEANS. Practice is one
// combined graph, so query(graph:"practice") with nothing else BROWSES it — the
// shape this message used to say did not exist. The enumeration survives only as
// the legacy read of the pre-singleton graphs, reached by mode:"modules", and it
// still takes no filters, which is what this message now says.
const practiceListGraphsUnrouted = "the practice-graph ENUMERATION lists the pre-singleton graphs and their sizes, " +
	"so it routes no browse filter at all. " +
	"To browse the combined practice graph, drop the enumeration: query(graph:\"practice\", type:...), " +
	"optionally narrowed to one origin with source:\"<hub id>\". " +
	"To browse ONE legacy graph, name it: query(graph:\"practice\", language:\"<lang>\", type:...). " +
	"The enumeration itself takes no filters"

// practiceFanOutRetired is the refusal for the language:"all" fan-out sentinel.
//
// THE SENTINEL IS RETIRED RATHER THAN RENAMED. It meant "search every practice
// graph", and there is one now, so the whole-corpus search is what an unselected
// call already does. Refusing rather than quietly treating "all" as the empty
// selector is the point: a caller sending it is asking for a scatter-gather that
// no longer exists, and answering a different question silently is the coercion
// this repo does not do.
//
// It is shaped on rankedSearchRetiredResult — say what is retired, say why, and
// name the call that works.
const practiceFanOutRetired = "language:\"all\" is retired: the practice family is ONE combined graph now, " +
	"so there is nothing left to fan out across and the sentinel would answer a different question than it was asked. " +
	"Omit the selector to search the whole practice corpus — query(graph:\"practice\", text:\"<query>\") — " +
	"or narrow to one origin with source:\"<hub id>\". " +
	"To search ONE pre-singleton graph, name it: query(graph:\"practice\", language:\"<lang>\", text:\"<query>\")"

// practiceStatsNoHubScope is the rejection reason for `source` on the practice
// stats arm.
//
// The generic tail ("this path does not route it") would be true and useless:
// this arm's whole output is counts, so a reader needs to know that the numbers
// are whole-graph BY CONSTRUCTION rather than that one param went unread.
const practiceStatsNoHubScope = "practice stats answers with whole-graph counts computed server-side, " +
	"so there is no per-hub arithmetic behind it to narrow and `source` would leave the numbers unchanged while looking applied. " +
	"To count one hub's nodes, browse it: query(graph:\"practice\", source:\"<hub id>\", type:...) and read the total"

// styleIndexPagingRejected is the rejection reason for `limit` and `offset` on
// the style-rule index arm.
//
// THE INDEX DRAINS, AND THAT IS WHY A PAGING WINDOW IS REFUSED RATHER THAN
// ROUTED. Its whole claim is "these are the rules that bind here", so a page
// boundary would turn rules a reader is relying on into rules nobody was told
// about — the caller could not tell a short window from a short corpus. The
// narrowing on offer is by scope, which removes rules that genuinely do not
// apply, rather than by position, which removes rules that do.
const styleIndexPagingRejected = "the style-rule index DRAINS every matching rule, because a page boundary " +
	"would silently drop rules that bind to the caller's paths and nothing in the output would say so. " +
	"Narrow it by SCOPE instead — query(graph:\"practice\", mode:\"style_index\", source:\"<hub id>\", " +
	"repo:\"<repo>\", path_prefixes:[\"<path>\"]) — which drops only the rules that do not apply"
