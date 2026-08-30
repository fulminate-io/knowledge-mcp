// SPDX-License-Identifier: Apache-2.0

package tools

// practice_selector_messages.go holds the three practice-selector refusal
// wordings. They live in a sibling file rather than beside the code that emits
// them because their two homes — intercept_query_practice_linkage.go and
// query_arm_registry_graphs.go — are both within a handful of lines of the repo's
// 500-line file convention, which is the split rule query_arm_registry.go's own
// header states.
//
// ALL THREE FOLLOW ONE SHAPE, taken from transformersSearchUnavailableResult
// (intercept_search_reducible_graph.go): say what the graph IS, say what is not on
// offer, and NAME THE CALL THAT WORKS. The generic accounting tail — "drop it or
// issue a separate call that does" — is true and useless here, because the caller
// does not know that supplying `language` IS the separate call.

// practiceListGraphsUnrouted is the rejection explanation for every browse-shaped
// param on the list-graphs arm, on the justifyRulesKnowledgeOnly precedent: the
// generic tail is replaced wherever a specific working call exists.
//
// graph:"practice" with NO language is the graph ENUMERATION, not a browse over
// every practice graph — there is no cross-language browse, and this message does
// not imply one. Supplying `language` selects a graph and gets a real browse.
const practiceListGraphsUnrouted = "graph:\"practice\" with no language is the practice-graph ENUMERATION " +
	"(it lists the loaded graphs and their sizes), so it routes no browse filter at all. " +
	"To browse ONE practice graph, name it: query(graph:\"practice\", language:\"<lang>\", type:...). " +
	"The enumeration itself takes no filters"

// practiceByIDNeedsLanguage is the refusal for a by-id read carrying no language.
// The practice family keys its instance BY language, so a by-id read with no
// language names no graph and can resolve nowhere — the server's own selector
// refuses it for that reason. It is claimed client-side purely to say which call
// works, because a caller holding a pattern id genuinely does not know which of
// the loaded practice graphs holds it. That turns a dead end into two calls.
//
// It names BOTH spellings because it covers both: the refusal fires for `id` and
// for `ids`, and a message naming only one reads as though the other were served.
const practiceByIDNeedsLanguage = "graph:\"practice\" keys its instance by language, so a by-id read " +
	"with no language names no graph to read from. Supply it — " +
	"query(graph:\"practice\", language:\"<lang>\", id:\"<node-id>\") for one node, or the same call " +
	"carrying ids:[...] for a bulk hydrate. If you do not know which practice graph holds them, " +
	"query(graph:\"practice\", mode:\"modules\") enumerates the loaded graphs"

// practiceFanOutNeedsText is the refusal for a TEXT-LESS language:"all" call.
//
// It is a DEFECT rather than an ergonomic gap: language:"all" reaches the ranked
// search fan-out, and an empty-text ranked search cannot return anything, so the
// zero it answered with was vacuous — a confident empty result from a scatter-
// gather that read every loaded practice graph. That is the same silent-zero class
// the transformers refusal was written to prevent.
const practiceFanOutNeedsText = "query(graph:\"practice\", language:\"all\") is the ranked-search fan-out " +
	"across every loaded practice graph, and a ranked search with no text has nothing to rank — it would " +
	"answer with a confident zero rather than an empty corpus. Supply text to search " +
	"— query(graph:\"practice\", language:\"all\", text:\"<query>\") — or, to BROWSE instead of search, " +
	"name one graph: query(graph:\"practice\", language:\"<lang>\", type:...). " +
	"query(graph:\"practice\", mode:\"modules\") enumerates the loaded practice graphs"
