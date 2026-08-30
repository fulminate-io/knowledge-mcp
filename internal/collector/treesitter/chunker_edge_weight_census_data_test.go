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

var edgeWeightConsumerCensus = []weightReaderRow{
	{
		Path:        "cmd/knowledge/internal/collector/treesitter/chunker_edges.go",
		Disposition: dispositionProducer,
		Reason: "weightedCallEdges ORIGINATES the value — it stamps the per-callee call count on " +
			"every call edge it builds. It consumes nothing.",
	},
	{
		Path:        "cmd/knowledge/internal/collector/contribhash/merge.go",
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
		Path:        "cmd/knowledge/internal/topology/foundation/adapter.go",
		Disposition: dispositionOptsIn,
		Reason: "THE GONUM FEED, and the row this whole decision turns on. materializeEdges reads " +
			"e.Weight, normalizes a zero to 1, and hands it to SetWeightedEdge — applying NO " +
			"edge-type and NO graph-type filter. That normalization is why an IMPLEMENTS edge " +
			"carries its method-set cardinality on Method instead: on Weight, the low-information " +
			"single-method edges would enter weighted centrality at the ordinary-edge baseline " +
			"while large interfaces were amplified, which is the opposite of the intent.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/graph_state_digest.go",
		Disposition: dispositionOptsIn,
		Reason: "THE ONE THAT IS NOT PLUMBING, and an ACCESSOR reader a `.Weight` grep cannot see. " +
			"edgeDigestRow renders an edge into the divergence digest as From, To, Type, " +
			"GetWeight(), GetConfidence(), GetMethod(), GetEvidence() and a tombstone marker, and " +
			"hashRows sorts and hashes the set. BOTH carriers are folded, so a change in an " +
			"interface's method-set cardinality moves the digest — correctly, the edge did change — " +
			"and it would have moved identically under a Weight-based carrier. The carrier choice " +
			"is digest-neutral.",
	},
	{
		Path:        "cmd/knowledge/internal/collector/parser/edges.go",
		Disposition: dispositionOptsIn,
		Reason: "The resolution arms copy Weight onto every RESOLVED reference edge. This is exactly " +
			"the path an IMPLEMENTS edge BYPASSES: the derivation appends fully-resolved edges " +
			"naming node IDs on both ends, which never enter resolveReference, so no IMPLEMENTS " +
			"edge ever reaches these lines.",
	},
	{
		Path:        "cmd/knowledge/internal/collector/parser/flow_edges.go",
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
		Path:        "cmd/knowledge/internal/collector/contribhash/contribhash.go",
		Disposition: dispositionOptsIn,
		Reason: "Folds Weight into the per-row contribution hash and into its sort key, exactly as it " +
			"folds Method. Churn is therefore identical under either carrier — choosing Method " +
			"neither adds nor removes re-upload.",
	},
	{
		Path:        "cmd/knowledge/internal/collector/parser/batchedges.go",
		Disposition: dispositionOptsIn,
		Reason:      "Wire conversion: copies Weight onto the batch edge as given.",
	},
	{
		Path:        "cmd/knowledge/internal/collector/remote/sink_metrics.go",
		Disposition: dispositionOptsIn,
		Reason: "An ACCESSOR reader. Its edgesFromProto DELIBERATELY mirrors the engine's decode arm " +
			"rather than importing it, to avoid the package dependency — see the engine row, which " +
			"cross-references this one, before 'de-duplicating' the two into a shared helper.",
	},
	{
		Path:        "cmd/knowledge/internal/engine/engine_decode.go",
		Disposition: dispositionOptsIn,
		Reason: "An ACCESSOR reader: the client-side proto decode arm. The collector's sink_metrics " +
			"arm is its deliberate mirror, kept separate to avoid a package dependency.",
	},
	{
		Path:        "cmd/knowledge-server/internal/bootstrap/engine_carrier_convert.go",
		Disposition: dispositionOptsIn,
		Reason: "An ACCESSOR reader: a server-side decode arm carrying edge metadata AS GIVEN. It " +
			"neither interprets nor clamps the value.",
	},
	{
		Path:        "cmd/knowledge-server/internal/bootstrap/engine_mutate_decode.go",
		Disposition: dispositionOptsIn,
		Reason:      "An ACCESSOR reader: the mutate decode arm, pass-through.",
	},
	{
		Path:        "cmd/knowledge-server/internal/bootstrap/engine_mutate_decode_predicate.go",
		Disposition: dispositionOptsIn,
		Reason: "An ACCESSOR reader, and it reads Weight off an EdgeSpec rather than an Edge — the " +
			"user-supplied side of the same field.",
	},
	{
		Path:        "cmd/knowledge/internal/crossgraph/crossgraph.go",
		Disposition: dispositionOptsIn,
		Reason: "A USER-FACING mutate(link, weight:) path: it copies a caller-supplied Weight onto the " +
			"edge spec, on ANY edge type in ANY graph. This is why scoping the weighted analyzers " +
			"to CALLS was declined — that would silently zero a deliberate user weighting on a " +
			"knowledge-graph edge, with no error and no signal.",
	},
	{
		Path:        "cmd/knowledge/internal/engine/compile_mutate_link.go",
		Disposition: dispositionOptsIn,
		Reason:      "A USER-FACING mutate(link, weight:) path; see the crossgraph row for the decline reason.",
	},
	{
		Path:        "cmd/knowledge/internal/tools/intercept_mutate_link.go",
		Disposition: dispositionOptsIn,
		Reason:      "A USER-FACING mutate(link, weight:) path; see the crossgraph row for the decline reason.",
	},
	{
		Path:        "cmd/knowledge/internal/externalcollector/convert.go",
		Disposition: dispositionOptsIn,
		Reason: "A FOURTH weight-supply surface, reaching in from outside the tree: the envelope's " +
			"`weight` JSON field is copied verbatim onto the wire edge.",
	},
	{
		Path:        "cmd/knowledge/internal/postpopulate/wire.go",
		Disposition: dispositionOptsIn,
		Reason:      "Surfaces a non-zero weight into a metadata map.",
	},
	{
		Path:        "cmd/knowledge/internal/engine/compile_mutate.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight through mutate compilation as given.",
	},
	{
		Path:        "cmd/knowledge/internal/engine/compile_mutate_create.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight through the create path as given.",
	},
	{
		Path:        "cmd/knowledge/internal/engine/dispatch_graphwide.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight on graph-wide dispatch as given.",
	},
	{
		Path:        "cmd/knowledge/internal/engine/edge_groups.go",
		Disposition: dispositionOptsIn,
		Reason:      "Reads Weight while grouping edges for display.",
	},
	{
		Path:        "cmd/knowledge/internal/engine/render_edge_metadata.go",
		Disposition: dispositionOptsIn,
		Reason:      "Renders Weight as edge metadata for a reader.",
	},
	{
		Path:        "cmd/knowledge/internal/kgwire/batchedge.go",
		Disposition: dispositionOptsIn,
		Reason:      "Wire carrier: Weight travels on the batch edge.",
	},
	{
		Path:        "cmd/knowledge/internal/paging/browse_drain.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight through a paged browse drain.",
	},
	{
		Path:        "cmd/knowledge/internal/paging/band_drain.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight through the from_id range-band edge drain, the same field-by-field copy its browse_drain.go sibling makes.",
	},
	{
		Path:        "cmd/knowledge/internal/tools/cross_graph_migrate.go",
		Disposition: dispositionOptsIn,
		Reason:      "Copies Weight when migrating an edge between graphs.",
	},
	{
		Path:        "cmd/knowledge/internal/tools/intercept_query_correlations_pivot.go",
		Disposition: dispositionOptsIn,
		Reason:      "Reads a correlation edge's Weight for the pivot rendering.",
	},
	{
		Path:        "cmd/knowledge/internal/tools/intercept_query_explain_timeline.go",
		Disposition: dispositionOptsIn,
		Reason:      "Reads a correlation edge's Weight for the explain/timeline rendering.",
	},
	{
		Path:        "cmd/knowledge/internal/tools/wire_persist.go",
		Disposition: dispositionOptsIn,
		Reason:      "Persists Weight on the wire edge as given.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/composite_db_branch_clone.go",
		Disposition: dispositionOptsIn,
		Reason: "The branch clone copies Weight verbatim onto the branch's copy of a base edge, alongside " +
			"Confidence, Method, Evidence, LastValidated and TombstonedAt. Every field is named explicitly " +
			"rather than copied wholesale, so the read is a deliberate pass-through of the stored value and " +
			"never an interpretation of what a weight means.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/composite_db_branch_reconcile.go",
		Disposition: dispositionOptsIn,
		Reason: "The branch RECONCILE's fill arm copies Weight verbatim onto the branch's copy of a base " +
			"edge the branch never received, alongside Confidence, Method, Evidence and LastValidated — " +
			"the same named-field pass-through the branch clone above performs, and for the same stated " +
			"reason (Edge embeds a protobuf message carrying a lock, so a wholesale copy is rejected). It " +
			"carries no TombstonedAt because it SKIPS tombstoned base edges outright rather than copying " +
			"their marker. The arm also DECIDES nothing on a weight — which base edges to copy is answered " +
			"by an identity set (from, to, type, evidence) and by the source edge's tombstoned state, so an " +
			"edge of any weight copies or skips identically. Its hosted twin carries the same field through " +
			"cloneEdgeColumns without naming Weight in Go at all.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/composite_db_write_batch_edges.go",
		Disposition: dispositionOptsIn,
		Reason: "Carries Weight through the composite write batch. The edge cluster was split out " +
			"of composite_db_write_batch.go into this file; the reader moved with it and the " +
			"disposition is unchanged.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/composite_db_edge_dedup.go",
		Disposition: dispositionOptsIn,
		Reason: "Edge dedup carries the incoming Weight onto the durable copy along with Confidence " +
			"and LastValidated. Pass-through with respect to the value: it decides WHICH edge " +
			"metadata survives a dedup, never what a weight means.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/contribution_hash.go",
		Disposition: dispositionOptsIn,
		Reason:      "Folds Weight into the server-side contribution hash.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/edge_iterator.go",
		Disposition: dispositionOptsIn,
		Reason:      "Reads Weight while iterating stored edges.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/graph_edges.go",
		Disposition: dispositionOptsIn,
		Reason:      "Reads and writes Weight on the stored edge.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/graph_serializer_edgemeta.go",
		Disposition: dispositionOptsIn,
		Reason:      "Serializes Weight as part of the edge metadata block.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/graph_traversal.go",
		Disposition: dispositionOptsIn,
		Reason:      "Carries Weight onto traversal results.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/graph_types.go",
		Disposition: dispositionOptsIn,
		Reason:      "Declares and reads the stored edge's Weight field.",
	},
	{
		Path:        "cmd/knowledge-server/internal/store/persistence_file_reads.go",
		Disposition: dispositionOptsIn,
		Reason:      "Reads Weight back off the persisted file format.",
	},

	// OVER-MATCH CLASS (1): thought-charge weight, an unrelated quantity.
	{
		Path:        "cmd/knowledge/internal/tools/intercept_thoughts_charge.go",
		Disposition: dispositionExcluded,
		Reason: "A CHARGE's weight, not an EDGE's — the significance of a piece of evidence attached " +
			"to a thought. It shares only the field name, and no edge reaches this file.",
	},
	{
		Path:        "cmd/knowledge/internal/tools/intercept_thoughts_simulate.go",
		Disposition: dispositionExcluded,
		Reason: "The same charge weight, in the simulate arm. Not an edge field, so the IMPLEMENTS " +
			"carrier decision cannot touch it.",
	},

	// OVER-MATCH CLASS (2): the gonum surface. These match the TYPE name
	// `.WeightedDirected...` or gonum's own `g.Weight(u, v)` METHOD, never an
	// edge field. None matches `GetWeight(` at all, so widening the detector left
	// this exclusion set untouched.
	{
		Path:        "cmd/knowledge/internal/topology/foundation/graph_methods.go",
		Disposition: dispositionExcluded,
		Reason: "Matches the gonum TYPE name only. The edge-field read that feeds gonum lives in " +
			"adapter.go, which is censused as a reader.",
	},
	{
		Path:        "cmd/knowledge/internal/topology/graph/betweenness.go",
		Disposition: dispositionExcluded,
		Reason:      "Matches the gonum weighted-graph TYPE name only; reads no edge field.",
	},
	{
		Path:        "cmd/knowledge/internal/topology/graph/betweenness_sampled.go",
		Disposition: dispositionExcluded,
		Reason:      "Matches the gonum weighted-graph TYPE name only; reads no edge field.",
	},
	{
		Path:        "cmd/knowledge/internal/topology/graph/hits.go",
		Disposition: dispositionExcluded,
		Reason:      "Matches the gonum weighted-graph TYPE name only; reads no edge field.",
	},
	{
		Path:        "cmd/knowledge/internal/topology/graph/pagerank_weighted.go",
		Disposition: dispositionExcluded,
		Reason:      "Matches the gonum weighted-graph TYPE name only; reads no edge field.",
	},
	{
		Path:        "cmd/knowledge/internal/topology/graph/pagerank_weighted_iteration.go",
		Disposition: dispositionExcluded,
		Reason: "Calls gonum's own g.Weight(u, v) METHOD to read an already-materialized graph's " +
			"weight. The value it reads came from adapter.go, which is the censused reader.",
	},
}
