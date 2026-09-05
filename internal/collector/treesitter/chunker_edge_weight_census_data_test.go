// SPDX-License-Identifier: Apache-2.0

package treesitter

// chunker_edge_weight_census_data_test.go holds the DISPOSITION TABLE that
// chunker_edge_weight_census_test.go's walk asserts against — split out of that
// file for the 500-line file cap. The walk, the two detectors, the named-file
// controls and the subtests all stay there; only the data moved.
//
// A ROW HERE IS A CLAIM READ IN CURRENT SOURCE, not a guess, and the census
// fails in BOTH directions: a Weight reader with no row fails, and a row whose
// file no longer reads Weight fails too.
//
// THIS TABLE COVERS THIS MODULE'S TREE ONLY, and its paths are MODULE-relative,
// because the file is published to the OSS mirror where the module root IS the
// repository root. The SERVER module's Weight readers are carried by
// chunker_edge_weight_census_server_test.go, which the sync script removes from
// the published tree. Both tables are walked in this repository, so the two
// halves together cover exactly what the single two-tree census covered.

var edgeWeightConsumerCensus = []weightReaderRow{
	{
		Path:        "internal/collector/treesitter/chunker_edges.go",
		Disposition: dispositionProducer,
		Reason: "weightedCallEdges ORIGINATES the value — it stamps the per-callee call count on " +
			"every call edge it builds. It consumes nothing.",
	},
	{
		Path:        "internal/collector/contribhash/merge.go",
		Disposition: dispositionOptsIn,
		Reason: "THE ONE READER THAT AGGREGATES RATHER THAN FORWARDS. MergeEdgesByIdentity " +
			"collapses copies of one stored edge identity and SUMS their Weight, because on " +
			"CALLS/TEST_CALLS the value is a call count whose copies each hold a share: " +
			"weightedCallEdges above aggregates call sites by callee SPELLING and does so " +
			"BEFORE resolution, so two spellings that bind to one target arrive here as two " +
			"rows. The summing is unconditional and needs no per-type allowlist because every " +
			"other collector edge type carries Weight 0 by construction, so summing zeros is " +
			"identical to keeping one copy.",
	},
	{
		Path:        "internal/topology/foundation/adapter.go",
		Disposition: dispositionOptsIn,
		Reason: "THE GONUM FEED, and the row this whole decision turns on. materializeEdges reads " +
			"e.Weight, normalizes a zero to 1, and hands it to SetWeightedEdge — applying NO " +
			"edge-type and NO graph-type filter. That normalization is why an IMPLEMENTS edge " +
			"carries its method-set cardinality on Method instead: on Weight, the low-information " +
			"single-method edges would enter weighted centrality at the ordinary-edge baseline " +
			"while large interfaces were amplified, which is the opposite of the intent.",
	},
	{
		Path:        "internal/collector/parser/edges.go",
		Disposition: dispositionOptsIn,
		Reason: "The resolution arms copy Weight onto every RESOLVED reference edge. This is exactly " +
			"the path an IMPLEMENTS edge BYPASSES: the derivation appends fully-resolved edges " +
			"naming node IDs on both ends, which never enter resolveReference, so no IMPLEMENTS " +
			"edge ever reaches these lines.",
	},
	{
		Path:        "internal/collector/parser/flow_edges.go",
		Disposition: dispositionOptsIn,
		Reason: "The flow arms copy Weight onto every resolved flow edge, exactly as the reference " +
			"arms beside them do. THE VALUE IS ALWAYS ZERO ON THIS PATH, and the copy is " +
			"deliberate anyway: the chunker stamps no count on a flow edge — a fact is about a " +
			"PARAMETER rather than a call site, so there is nothing to count — and forwarding the " +
			"field rather than omitting it keeps these arms structurally identical to the " +
			"reference arms beside them, so a later producer change cannot be silently dropped " +
			"here. A cardinality must never be moved onto Weight for these types; the gonum feed " +
			"row above records why.",
	},
	{
		Path:        "internal/collector/contribhash/contribhash.go",
		Disposition: dispositionOptsIn,
		Reason: "Folds Weight into the per-row contribution hash and into its sort key, exactly as it " +
			"folds Method. Churn is therefore identical under either carrier — choosing Method " +
			"neither adds nor removes re-upload.",
	},
	{
		Path:        "internal/collector/parser/batchedges.go",
		Disposition: dispositionOptsIn,
		Reason:      "Wire conversion: copies Weight onto the batch edge as given.",
	},
	{
		Path:        "internal/collector/remote/sink_metrics.go",
		Disposition: dispositionOptsIn,
		Reason: "An ACCESSOR reader. Its edgesFromProto DELIBERATELY mirrors the engine's decode arm " +
			"rather than importing it, to avoid the package dependency — see the engine row, which " +
			"cross-references this one, before 'de-duplicating' the two into a shared helper.",
	},
	{
		Path:        "internal/engine/engine_decode.go",
		Disposition: dispositionOptsIn,
		Reason: "An ACCESSOR reader: the client-side proto decode arm. The collector's sink_metrics " +
			"arm is its deliberate mirror, kept separate to avoid a package dependency.",
	},
	{
		Path:        "internal/crossgraph/crossgraph.go",
		Disposition: dispositionOptsIn,
		Reason: "A USER-FACING mutate(link, weight:) path: it copies a caller-supplied Weight onto the " +
			"edge spec, on ANY edge type in ANY graph. This is why scoping the weighted analyzers " +
			"to CALLS was declined — that would silently zero a deliberate user weighting on a " +
			"knowledge-graph edge, with no error and no signal.",
	},
	{
		Path:        "internal/engine/compile_mutate_link.go",
		Disposition: dispositionOptsIn,
		Reason:      "A USER-FACING mutate(link, weight:) path; see the crossgraph row for the decline reason.",
	},
	{
		Path:        "internal/tools/intercept_mutate_link.go",
		Disposition: dispositionOptsIn,
		Reason:      "A USER-FACING mutate(link, weight:) path; see the crossgraph row for the decline reason.",
	},
	{
		Path:        "internal/externalcollector/convert.go",
		Disposition: dispositionOptsIn,
		Reason: "A FOURTH weight-supply surface, reaching in from outside the tree: the envelope's " +
			"`weight` JSON field is copied verbatim onto the wire edge.",
	},
	{
		Path:        "internal/postpopulate/wire.go",
		Disposition: dispositionOptsIn,
		Reason:      "Surfaces a non-zero weight into a metadata map.",
	},
	{
		Path:        "internal/engine/compile_mutate.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight through mutate compilation as given.",
	},
	{
		Path:        "internal/engine/compile_mutate_create.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight through the create path as given.",
	},
	{
		Path:        "internal/engine/dispatch_graphwide.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight on graph-wide dispatch as given.",
	},
	{
		Path:        "internal/engine/edge_groups.go",
		Disposition: dispositionOptsIn,
		Reason:      "Reads Weight while grouping edges for display.",
	},
	{
		Path:        "internal/engine/render_edge_metadata.go",
		Disposition: dispositionOptsIn,
		Reason:      "Renders Weight as edge metadata for a reader.",
	},
	{
		Path:        "internal/kgwire/batchedge.go",
		Disposition: dispositionOptsIn,
		Reason:      "Wire carrier: Weight travels on the batch edge.",
	},
	{
		Path:        "internal/paging/browse_drain.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight through a paged browse drain.",
	},
	{
		Path:        "internal/paging/band_drain.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight through the from_id range-band edge drain, the same field-by-field copy its browse_drain.go sibling makes.",
	},
	{
		Path:        "internal/tools/cross_graph_migrate.go",
		Disposition: dispositionOptsIn,
		Reason:      "Copies Weight when migrating an edge between graphs.",
	},
	{
		Path:        "internal/tools/intercept_query_correlations_pivot.go",
		Disposition: dispositionOptsIn,
		Reason:      "Reads a correlation edge's Weight for the pivot rendering.",
	},
	{
		Path:        "internal/tools/intercept_query_explain_timeline.go",
		Disposition: dispositionOptsIn,
		Reason:      "Reads a correlation edge's Weight for the explain/timeline rendering.",
	},
	{
		Path:        "internal/tools/wire_persist.go",
		Disposition: dispositionOptsIn,
		Reason:      "Persists Weight on the wire edge as given.",
	},
	{
		Path:        "internal/tools/intercept_thoughts_charge.go",
		Disposition: dispositionExcluded,
		Reason: "A CHARGE's weight, not an EDGE's — the significance of a piece of evidence attached " +
			"to a thought. It shares only the field name, and no edge reaches this file.",
	},
	{
		Path:        "internal/tools/intercept_thoughts_simulate.go",
		Disposition: dispositionExcluded,
		Reason: "The same charge weight, in the simulate arm. Not an edge field, so the IMPLEMENTS " +
			"carrier decision cannot touch it.",
	},
	{
		Path:        "internal/topology/foundation/graph_methods.go",
		Disposition: dispositionExcluded,
		Reason: "Matches the gonum TYPE name only. The edge-field read that feeds gonum lives in " +
			"adapter.go, which is censused as a reader.",
	},
	{
		Path:        "internal/topology/graph/betweenness.go",
		Disposition: dispositionExcluded,
		Reason:      "Matches the gonum weighted-graph TYPE name only; reads no edge field.",
	},
	{
		Path:        "internal/topology/graph/betweenness_sampled.go",
		Disposition: dispositionExcluded,
		Reason:      "Matches the gonum weighted-graph TYPE name only; reads no edge field.",
	},
	{
		Path:        "internal/topology/graph/hits.go",
		Disposition: dispositionExcluded,
		Reason:      "Matches the gonum weighted-graph TYPE name only; reads no edge field.",
	},
	{
		Path:        "internal/topology/graph/pagerank_weighted.go",
		Disposition: dispositionExcluded,
		Reason:      "Matches the gonum weighted-graph TYPE name only; reads no edge field.",
	},
	{
		Path:        "internal/topology/graph/pagerank_weighted_iteration.go",
		Disposition: dispositionExcluded,
		Reason: "Calls gonum's own g.Weight(u, v) METHOD to read an already-materialized graph's " +
			"weight. The value it reads came from adapter.go, which is the censused reader.",
	},
}
