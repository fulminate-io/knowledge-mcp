// SPDX-License-Identifier: Apache-2.0

package kgtypes

// Shared metadata-key vocabulary. These literal key strings are SHARED WIRE
// VOCABULARY: the client writes them onto node / graph metadata and the server
// reads the SAME literal keys back off that metadata (and vice versa). They
// therefore live in kgtypes — the wire-vocab leaf — alongside the
// NodeType/EdgeType/GraphType const vocabulary.

// LLM-pipeline failure marker keys. The pipeline v2 worker writes these keys
// onto a node's metadata when the LLM call returns a TERMINAL error
// (4xx-other / context-too-large / config error — anything that won't be
// resolved by retrying). Discovery shims subsequently exclude nodes whose
// marker is non-empty so a single bad node never burns infinite worker time.
//
// Operator recovery path: manage(operation="clear_llm_failures") resets these
// markers across one or all loaded graphs and the node re-enters the
// discovery pipeline on the next collector tick.
//
// Transient errors (429 / 5xx / network) DO NOT write a marker — they get
// retried on the next tick by the natural discovery loop.
const (
	// MetaKeySummaryFailureReason is the metadata key that marks a node as
	// having failed terminal-error summarization. Empty value = no failure
	// (re-eligible). Non-empty value = stop attempting; operator must clear
	// via manage(clear_llm_failures).
	MetaKeySummaryFailureReason = "summary_failure_reason"

	// MetaKeyEmbedFailureReason is the metadata key that marks a node as
	// having failed terminal-error embedding. Same operator-clear contract
	// as MetaKeySummaryFailureReason.
	MetaKeyEmbedFailureReason = "embed_failure_reason"

	// MetaKeySegmentShipFailureReason records that a node's binary vector was
	// written but its client segment ship was DROPPED. It is deliberately a
	// SEPARATE key from MetaKeyEmbedFailureReason: both rebuild scans exclude
	// embed-failure-marked nodes, so reusing that key would make a ship-dropped
	// node permanently invisible to the very repair that exists to re-ship it.
	MetaKeySegmentShipFailureReason = "segment_ship_failure_reason"
)

// Code-graph metadata keys read once by the client-side collector and shipped
// over the wire: the server is filesystem-blind for
// code-graph paths, so topology analyzers read these off graph metadata
// instead of opening go.mod / .knowledge/topology_layers.yaml on the pod.
const (
	// ModulePathKey stores the Go module path for a code graph. Read once by
	// the client-side collector from `<rootDir>/go.mod`; pkg/topology/dsm.go
	// reads it server-side in lieu of opening go.mod.
	ModulePathKey = "kgmeta\x00module_path"

	// LayerConfigKey stores the raw YAML body of
	// `.knowledge/topology_layers.yaml` when the file exists in the repo.
	// Same client-side-read / server-graph-read split as ModulePathKey.
	LayerConfigKey = "kgmeta\x00layer_config"
)

// Practice hub keys. The practice family is ONE combined graph, so a node's
// origin is recorded on the node rather than by which graph it lives in.
const (
	// MetaKeySourceHub carries the id of the `source` hub node a practice node is
	// grouped under. It is the key every practice-arm `source` selector lowers
	// onto: a browse and a delete narrow by an equality predicate on it, and the
	// ranked search resolves the hub's member ids through it before searching.
	//
	// IT IS DELIBERATELY NOT SPELLED `source`, and that is a measured constraint
	// rather than a preference. Practice nodes ALREADY carry a `source` metadata
	// key — 237 of them in one graph alone, holding values like a book or catalog
	// slug — so a hub predicate spelled `source` would match nodes that have no
	// hub at all. The node's top-level `source` FIELD is a third thing again
	// (writer identity, a recipe name, or empty) and is untouched by any of this.
	//
	// The server additionally force-edges the metadata key `source` for one
	// practice graph's value-node promotion, which is a second, independent
	// reason the two must not share a spelling.
	MetaKeySourceHub = "source_hub"

	// MetaKeySourceHubKind marks WHICH KIND of origin a `source` hub node is:
	// a language, a catalog, or a collected run. It lives on the HUB, never on
	// the members.
	MetaKeySourceHubKind = "kind"
)

// Style-rule keys. A STYLE RULE is a practice node under a language hub whose
// text states a code style requirement — "do not use X", "write your for loops
// this way", "guidance on package shape". It is an ordinary `pattern` node, so
// it inherits the pattern body's mandatory author-supplied name and summary; the
// keys below are what make it a style rule rather than a prose pattern, and what
// a reader narrows by.
//
// THEY ARE PREFIXED `style_` FOR THE SAME REASON MetaKeySourceHub IS NOT SPELLED
// `source`. Practice is ONE combined graph now, holding every kind of guidance
// for every language, so an unprefixed scope family (`scope_repo`, `scope_paths`)
// would be a key whose meaning depends on which practice kind happens to carry
// it. The prefix ties each key to the MetaKeyPracticeKind value that gates it.
const (
	// MetaKeyPracticeKind names WHICH KIND of practice node this is, so one
	// combined graph can hold several and a browse can narrow to one by
	// equality.
	//
	// IT IS NOT SPELLED `kind`, and that is measured rather than preferred.
	// `kind` is already MetaKeySourceHubKind on the hub node, so the two
	// questions would share one spelling inside one graph; and the
	// design-patterns practice corpus already carries `kind` on 32 of its own
	// nodes, holding values that have nothing to do with this vocabulary.
	MetaKeyPracticeKind = "practice_kind"

	// PracticeKindStyleRule is the MetaKeyPracticeKind VALUE that marks a style
	// rule. It is a value rather than a presence key on purpose: a bare
	// `style_rule:"true"` marker could never name a SECOND practice kind later,
	// and a browse costs the same for an equality predicate as for a presence
	// one.
	PracticeKindStyleRule = "style_rule"

	// MetaKeyStyleScopeRepo narrows a style rule to ONE repository by name.
	// ABSENT MEANS EVERY REPOSITORY — the scope is optional, so an unscoped rule
	// is language-wide and must appear in every index. That disjunction is why
	// the scope narrowing is applied client-side: metadata predicates are
	// chained conjunctively server-side and cannot express "absent OR equal".
	MetaKeyStyleScopeRepo = "style_scope_repo"

	// MetaKeyStyleScopePaths narrows a style rule to one or more repo-relative
	// path prefixes, matched at PATH-SEGMENT boundaries. ABSENT MEANS EVERY PATH.
	//
	// ITS VALUE IS A JSON ARRAY, never a comma join. A repo-relative path may
	// legally contain a comma, and the corpus walk admits and scans one, so a
	// comma-joined value would silently split one legitimate path into two that
	// do not exist. A JSON array's uncarryable set is empty, so it owes no
	// refusal of that class.
	MetaKeyStyleScopePaths = "style_scope_paths"

	// MetaKeyLinterName and MetaKeyLinterRuleID record where a style rule came
	// from when it was derived from a linter's configuration.
	//
	// TWO KEYS RATHER THAN ONE PACKED VALUE. A packed "golangci:modernize" forces
	// a parse on a separator a linter rule id may itself contain, which is the
	// same class the path key avoids above, and bad input must error rather than
	// coerce.
	MetaKeyLinterName   = "linter_name"
	MetaKeyLinterRuleID = "linter_rule_id"

	// MetaKeySisterCheck, on a PRACTICE node, holds the id of the check that
	// enforces this rule; MetaKeySisterPractice, on a CHECK node, holds the id of
	// the practice node it enforces. The pair is the cross-link, and it is
	// METADATA rather than an edge because a check's only edges are display-only
	// fixture bindings and nothing in the tree links a practice node to a check
	// at all — there is no edge mechanism to reuse, and a metadata key is what a
	// browse can predicate on and an index render can read.
	//
	// NOTHING IN THIS PACKAGE'S CONSUMERS WRITES EITHER KEY. The pair is filled
	// by the author of the sister check, in its own tool call, after an LLM has
	// passed on the practice node; no import, batch, landing or migration creates
	// a check or fills this link. The vocabulary is declared here so both sides
	// spell it once.
	//
	// It is NOT the existing `sibling_check`, which occurs once in the corpus and
	// means check-to-check: overloading that spelling would give one key two
	// referent types across two graphs.
	MetaKeySisterCheck    = "sister_check"
	MetaKeySisterPractice = "sister_practice"
)
