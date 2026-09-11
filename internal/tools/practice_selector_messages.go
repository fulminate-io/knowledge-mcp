// SPDX-License-Identifier: Apache-2.0

package tools

// practice_selector_messages.go holds the practice-selector refusal wordings.
// They live in a sibling file rather than beside the code that emits them
// because their two homes — intercept_query_practice_linkage.go and
// query_arm_registry_graphs.go — are both within a handful of lines of the repo's
// 500-line file convention, which is the split rule query_arm_registry.go's own
// header states.
//
// THEY ALL FOLLOW ONE SHAPE, the same one rankedSearchRetiredResult
// (intercept_search_reducible_graph.go) uses: say what the graph IS, say what is
// not on offer, and NAME THE CALL THAT WORKS. The generic accounting tail — "drop it or
// issue a separate call that does" — is true and useless here, because the caller
// does not know that supplying `language` IS the separate call.

// practiceListGraphsUnrouted is the rejection explanation for every browse-shaped
// param on the list-graphs arm, on the justifyRulesKnowledgeOnly precedent: the
// generic tail is replaced wherever a specific working call exists.
//
// THE ENUMERATION IS NO LONGER WHAT AN EMPTY SELECTOR MEANS. Practice is one
// combined graph, so query(graph:"practice") with nothing else BROWSES it — the
// shape this message used to say did not exist. The enumeration survives as the
// CATALOG read, reached by mode:"modules", answering whether the graph exists
// and how big it is; it still takes no filters, which is what this message says.
//
// IT NAMES NO LEGACY GRAPH ANY MORE. It used to close by telling a caller how to
// browse ONE of the pre-singleton graphs with `language`, which would now send
// them at a refused selector.
const practiceListGraphsUnrouted = "the practice-graph ENUMERATION reads the CATALOG — whether the graph exists and its size — " +
	"so it routes no browse filter at all. " +
	"To browse the practice graph, drop the enumeration: query(graph:\"practice\", type:...), " +
	"optionally narrowed to one origin with source:\"<hub id>\". " +
	"The enumeration itself takes no filters"

// THE language:"all" FAN-OUT REFUSAL WENT WITH EVERY OTHER VALUE OF THE FIELD.
// practiceFanOutRetired was the one message here that named a single value: the
// sentinel meant "search every practice graph", there was one, and refusing
// rather than quietly treating "all" as the empty selector was the point. Every
// value of `language` is refused on a practice read now, with one spelling
// (practiceLanguageRefusedOnRead), so a per-value message would have been a
// second wording for a rule the caller cannot reach two ways.

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
