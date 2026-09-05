// SPDX-License-Identifier: Apache-2.0

package tools

// truncation_disclosure_census_test.go is the standing structural gate against a
// read path that renders a bounded result without saying it was bounded. The
// go/ast walk and the member rule live in truncation_disclosure_scan_test.go;
// this file holds the declaration table and the gate that reads it.
//
// WHY STRUCTURAL RATHER THAN A HAND LIST: an ast count of the inline
// `exec(ctx, &knowledgev1.ExecuteRequest{...})` shape alone returns 50 sites
// across 26 files in the tools package, and that shape UNDER-REPORTS arms that
// build the request through a variable or a helper. A surface this size is
// pattern-defined; a hand list rots the first time an arm is added. Modeled on
// bootstrap/bounded_reads_census_test.go (survivor-with-a-written-reason) and on
// cloud/persistence/postgres/sqlcost_census_test.go (walk + self-check split).
//
// TWO AXES, INDEPENDENT OF EACH OTHER. Axis one is about a PROSE BLOCK reaching
// the caller; axis two is about a KEY IN A JSON PAYLOAD. A row's value on one
// says nothing about its value on the other — InterceptQueryPlanTree is the
// proof: it discloses for itself in prose on both formats, and its JSON envelope
// carried no key at all until this ticket.

const (
	// disclosureHandles: the site discloses the verdict TO THE CALLER ITSELF.
	// Normally that means calling a declared truncation-disclosure helper, and
	// there are exactly TWO, so the reason must name WHICH:
	// engine.WithTruncationNotice, for every arm that discloses for itself, and
	// tools.withTruncationNotice, permitted for the plan_tree arm ALONE because
	// its product copy deliberately differs (a tree has no pages to walk, and
	// plan_tree's `limit` IS the subtree depth). A site that REFUSES the read
	// outright on a truncated verdict also belongs here: the axis is about
	// WHETHER the site discloses for itself, never about which function name it
	// calls, and a named refusal is the loudest disclosure there is.
	disclosureHandles = "handles"
	// disclosureByCaller: the site is truncatable and correctly does NOT disclose
	// for itself, because a named wrapper discloses for it. The reason MUST name
	// that wrapper, and the named wrapper must itself be a row here classified
	// "handles" — enforced by the chain_termination sub-test. That rule is what
	// stops this value becoming a universal excuse.
	disclosureByCaller = "disclosed-by-caller"
	// disclosureCannot: the site's read cannot come back truncated at all. The
	// reason names the structural fact, never just the conclusion.
	disclosureCannot = "cannot-truncate"

	// carrierYes: the site builds a JSON envelope for a row-bounded READ, so that
	// envelope MUST emit the `truncated` key — enforced by json_carriers. The
	// reason states whether the verdict is LIVE (read off the response) or CONSTANT
	// BY CONSTRUCTION, and a constant one must contain the phrase
	// constantByConstruction below plus the structural fact that makes false true.
	// Two arms are legitimately constant: the search envelope, whose verdict source
	// ExecuteResponse.SearchResults is unpopulated server-side, and the examine
	// envelope, whose edge drain never returns a partial union.
	carrierYes = "yes"
	// carrierNo: the site builds a JSON envelope that is not a row-bounded read's
	// payload — a mutation outcome, a catalog, a statistics body; the reason names
	// which.
	carrierNo = "no"
	// carrierNA: the site builds no JSON envelope OF ITS OWN — a text/markdown
	// arm, or a pure delegator whose reason names the row that does build it.
	carrierNA = "n-a"

	// constantByConstruction is the phrase a json_carrier "yes" row must contain
	// when its key is a constant rather than a read verdict. It is deliberately a
	// PHRASE and not a boolean field: the point is to force the author to write the
	// structural fact next to it, so a later reader can check whether that fact
	// still holds rather than re-deriving why the constant was ever acceptable.
	constantByConstruction = "CONSTANT BY CONSTRUCTION"
)

// disclosureRow is one site's declaration on both axes. Both reasons are
// mandatory: a bare classification records a verdict without recording what a
// later reader would have to re-check to know it still holds.
type disclosureRow struct {
	disclosure string
	reason     string
	carrier    string
	carrierWhy string
}

// minCensusPopulation is the floor the scan must clear. A member rule that
// silently narrows is the same defect class this gate is about, so the census
// asserts its own population rather than trusting it: a rule that stops matching
// reports a comfortable clean scan over a surface it can no longer see. This is
// a measured FLOOR, not the exact population.
const minCensusPopulation = 17

// truncationDisclosureSites declares every census member, keyed
// "<basename>.go:<funcName>".
//
// The three recurring structural facts the reasons below lean on, stated once
// here and read first-hand in cmd/knowledge-server/internal/bootstrap:
//   - executeTruncation (engine_limits.go:129) sets the flag from
//     ceilingEngaged(requested, ceiling, rowCount), which is
//     `clamped && rowCount >= effective`. A read that FILLS the ceiling flags; a
//     by-id read returning one row never can.
//   - resultRowCount (engine.go:299) counts NodeList, IDList and TraversalList
//     only. A carrier outside those three — graph names, graph stats — cannot
//     make the comparison true.
//   - The id-set and pivot-set bounds are the exceptions to filled-to-the-
//     ceiling: `len(p.GetIds()) > maxExecuteNodeRows` flags on the request
//     alone, so a bulk ids[] hydrate over more than 10,000 ids IS truncatable.
//
// THE EDGE DRAIN IS NOT THE ONLY READ THESE ARMS ISSUE, and an earlier revision
// of this note said it was. InterceptQueryExamine and dispatchQueryByID both end
// their composition in ONE unbounded bulk hydrate over the drained union's peer
// set, which the server clamps on the id-set bound above — so both DO receive a
// real verdict, both are clause (a2) members, and both are declared below. The
// drain property is still true of the EDGES and is pinned by
// TestExamine_EdgeNeighborhoodIsCompleteOrLoud and
// TestByIDEdgeSummary_CompleteOrLoud; the hydrate verdict is pinned by
// TestExamine_TruncatedField and TestByIDPeerHydrate_TruncationNotice.
var truncationDisclosureSites = map[string]disclosureRow{
	// --- engine: the Render spine -------------------------------------------
	"dispatch.go:Dispatch": {
		disclosureByCaller,
		"the generic compile/exec path ends in engine.Render, which appends the notice to every response Dispatch returns through it; the three pre-Compile seams it delegates to (dispatchQueryByID, dispatchGraphWideEdges, dispatchDeletePreview) each carry their own row",
		carrierNA,
		"builds no envelope: it returns whatever Render or an intercept seam built",
	},
	"dispatch.go:Render": {
		disclosureHandles,
		"calls engine.WithTruncationNotice on every rendered response; Render is THE single function through which a compiled response becomes a ToolResult, so it is the disclosing ancestor for every renderer below it",
		carrierNA,
		"appends a trailing content block; the envelope is built by the per-tool renderer it wraps",
	},
	"dispatch.go:WithTruncationNotice": {
		disclosureHandles,
		"it IS engine.WithTruncationNotice, the declared engine-side disclosure helper — the single declaration of the server-row-ceiling sentence",
		carrierNA,
		"appends a trailing text block; it never touches a payload",
	},
	"dispatch.go:renderByTool": {
		disclosureByCaller,
		"Render's own per-tool dispatch: it runs inside engine.Render, which appends the notice to what it returns",
		carrierNA,
		"delegates to the per-tool renderers, which carry their own rows",
	},
	"dispatch.go:renderSearchTool": {
		disclosureByCaller,
		"runs under engine.Render via renderByTool",
		carrierNA,
		"delegates to renderSearchResponse / renderSearchResponseFiltered",
	},
	"dispatch.go:renderQueryTool": {
		disclosureByCaller,
		"runs under engine.Render via renderByTool",
		carrierNA,
		"delegates to renderBrowseResponse / renderNodesByIDsResponse / renderNodeResponse",
	},
	"dispatch.go:renderTraverseTool": {
		disclosureByCaller,
		"runs under engine.Render via renderByTool",
		carrierNA,
		"delegates to renderTraversalResponse",
	},
	"dispatch.go:renderMutateTool": {
		disclosureByCaller,
		"runs under engine.Render via renderByTool",
		carrierNA,
		"delegates to renderMutationResponse",
	},
	"dispatch.go:renderDeleteTool": {
		disclosureByCaller,
		"runs under engine.Render via renderByTool",
		carrierNA,
		"delegates to renderMutationResponse",
	},
	"dispatch.go:renderGraphNamesResponse": {
		disclosureCannot,
		"reads the RETURN_MODE_GRAPH_NAMES catalog carrier, which is none of the three carriers resultRowCount counts, so rowCount is 0 and ceilingEngaged can never be true",
		carrierNo,
		"the {graphs:[...]} envelope enumerates loaded graph handles, not graph rows — the row ceiling does not reach it",
	},

	// --- engine: the pre-Compile intercept seams -----------------------------
	"dispatch_byid.go:dispatchQueryByID": {
		disclosureHandles,
		"bypasses engine.Render (Dispatch returns its result before Compile) and ends BOTH compositions in one unbounded bulk peer hydrate the server clamps above 10,000 ids — a clamp that renders edge peers under their id-prefix fallback name and DROPS cross-link rows outright; discloses via engine.WithTruncationNoticeFor on the OR of the two hydrate verdicts",
		carrierYes,
		"its format:\"json\" arm builds the {node, edges, cross_links, truncated} byIDJSONEnvelope through renderByIDResult, emitting the key LIVE from the OR of the two peer-hydrate verdicts. The two legacy bodies it also renders — renderKnowledgeNode's {node, edges} JSON and renderGenericNode's markdown — are the format-unset shapes and carry no key; query_schema.go states that split plainly rather than implying the key is everywhere",
	},
	"intercept_query_examine.go:InterceptQueryExamine": {
		disclosureHandles,
		"bypasses engine.Render, and composeInspectData ends in one unbounded bulk peer+ancestor hydrate the server clamps above 10,000 ids, leaving peers and ancestry rows with empty names; discloses via engine.WithTruncationNoticeFor on that verdict. The EDGE drain contributes nothing — it is complete-or-error",
		carrierYes,
		"buildInspectJSON's {id, name, type, ancestry, edges} envelope is the examine read's payload and emits the key LIVE from InspectData.Truncated, the bulk hydrate's own verdict",
	},
	"dispatch_delete_preview.go:dispatchDeletePreview": {
		disclosureHandles,
		"bypasses engine.Render (Dispatch returns its result before Compile) and its prune-by-age read carries NO Limit, so the ceiling engages at 10,000 rows and a 'would delete N' preview would understate the real delete; discloses via engine.WithTruncationNotice",
		carrierYes,
		"the {dry_run, would_delete, nodes} envelope is a row-bounded read's payload and carries the key",
	},
	"dispatch_graphwide.go:dispatchGraphWideJSON": {
		disclosureCannot,
		"a paging.DrainKeysetPages enumeration: every page carries an explicit Limit of paging.BrowsePageSize, below the server row ceiling, so clampRequestLimit never clamps and the drain pages until exhaustion; the edge union rides paging.DrainPivotEdges, which consumes its own verdict as a page-halving signal and errors by name rather than returning a short union",
		carrierNo,
		"renderGraphWideJSON's payload is assembled from a drained enumeration, so no ceiling verdict exists to report",
	},
	"dispatch_graphwide.go:dispatchGraphWideText": {
		disclosureCannot,
		"the ids-only twin of dispatchGraphWideJSON: same keyset drain at paging.BrowsePageSize, same DrainPivotEdges edge union",
		carrierNA,
		"renders counts as text",
	},

	// --- engine: the renderers under Render ----------------------------------
	"browse_selection_export.go:RenderBrowse": {
		disclosureByCaller,
		"the exported wrapper over renderBrowseResponse; its single caller is practiceBrowse, which discloses for itself, and the unexported original is reached under engine.Render for every other browse",
		carrierNA,
		"delegates verbatim to renderBrowseResponse, which is the declared carrier row",
	},
	"render_misc.go:renderBrowseResponse": {
		disclosureByCaller,
		"reached under engine.Render on the compiled browse path, and under practiceBrowse (via RenderBrowse) on the intercept path; both disclose for it",
		carrierYes,
		"builds the {graph, type, results, total} browse envelope through renderBrowseJSON, which the node row ceiling can clamp",
	},
	"render_misc.go:renderTraversalResponse": {
		disclosureByCaller,
		"reached under engine.Render via renderTraverseTool",
		carrierYes,
		"builds the {start, graph, direction, nodes, edges} traversal envelope, which rides the 50,000-row edges ceiling — the largest truncation surface in the system; group_reconstruction_incomplete answers a NARROWER question and is not a substitute",
	},
	"render_misc.go:renderNodesByIDsResponse": {
		disclosureByCaller,
		"reached under engine.Render via renderQueryTool",
		carrierYes,
		"builds the {label, nodes} bulk-hydrate envelope, which the node row ceiling clamps AND which flags on the id-set bound alone above 10,000 ids",
	},
	"render_misc.go:renderMutationResponse": {
		disclosureByCaller,
		"reached under engine.Render via renderMutateTool / renderDeleteTool",
		carrierNo,
		"the {ids} / {affected} envelope reports a mutation outcome, not a row-bounded read",
	},
	"render_node.go:renderNodeResponse": {
		disclosureCannot,
		"a by-id read returns one row against a 10,000-row ceiling, and ceilingEngaged requires rowCount >= effective",
		carrierNo,
		"the fields-projection arm emits one projected node; a single row cannot fill a ceiling",
	},
	"render_search.go:renderSearchResponse": {
		disclosureByCaller,
		"reached under engine.Render via renderSearchTool",
		carrierYes,
		"builds the {query, total, results} search envelope; the key is constant FALSE and TRUE BY CONSTRUCTION — the verdict would ride ExecuteResponse.SearchResults, which nothing server-side populates, so no ceiling can signal engagement on this arm",
	},
	"render_search.go:renderSearchResponseFiltered": {
		disclosureByCaller,
		"reached under engine.Render via renderSearchTool",
		carrierYes,
		"the resource_type-filtered twin of renderSearchResponse, emitting the same envelope on the same constant-false-by-construction terms",
	},

	// --- tools: the client intercepts ----------------------------------------
	"ast.go:handleAstCount": {
		disclosureCannot,
		"the ast tool walks the FILESYSTEM and issues no Execute at all; the ExcludedTruncated value it reads is a bounded sample-list flag on that walk, not a server row-ceiling verdict",
		carrierNo,
		"its excluded_truncated key reports the sample-list bound and is unrelated to a server read",
	},
	"intercept_manage_drop_graph.go:handleClientDropGraph": {
		disclosureCannot,
		"issues a MUTATION_KIND_DROP_GRAPH mutation, not a row-returning read",
		carrierNA,
		"renders the drop acknowledgement as text",
	},
	"intercept_mutate_create.go:handleClientUpdateStatusRollup": {
		disclosureHandles,
		"REFUSES on a truncated traversal: it returns rollupFailureResult with errRollupTraverseTruncated rather than rolling a status over a partial subtree, and a named refusal is the loudest disclosure there is",
		carrierNA,
		"its results are error / acknowledgement text",
	},
	// This row was keyed on handleClientCrossGraphLink until the intra-practice
	// fast path was extracted into its own function. The Execute went WITH the
	// extracted body, so the scanned site moved rather than disappeared, and the
	// declaration follows the code: handleClientCrossGraphLink now holds no
	// ExecuteResponse and is no longer a census member, while every read the old
	// row accounted for is still accounted for here or in an existing row.
	// Nothing left the census — the by-id endpoint probes ride
	// projects/render's own rows, and the foreign-graph enumeration reached
	// through crossgraph.ResolveAndLink was always outside these two
	// directories and carries its boundedness there.
	"intercept_mutate_link.go:intraPracticeLinkArm": {
		disclosureCannot,
		"its two reads are by-id endpoint probes in practice/<language> (one row each) and the in-practice link MUTATION, whose response is an acknowledgement; none of the three can be truncated, and this arm issues no enumeration at all",
		carrierNA,
		"renders link acknowledgements as text",
	},
	"intercept_query_analyze_node.go:composeAnalyzeNode": {
		disclosureHandles,
		"bypasses engine.Render, and its four traverseCallNodes walks carry NO Limit, so a deep code-graph walk that fills the 10,000-row traversal ceiling renders a complete-looking call graph with edges missing; discloses via engine.WithTruncationNoticeFor on the OR of the four verdicts",
		carrierNA,
		"renders the analyze view as text",
	},
	"intercept_query_cloud_cicd.go:resourceGetNode": {
		disclosureCannot,
		"a by-id read of one resource node; ceilingEngaged requires rowCount >= effective",
		carrierNA,
		"renders one resource node as markdown",
	},
	"intercept_query_cloud_cicd.go:resourceBrowse": {
		disclosureHandles,
		"bypasses engine.Render (it issues its own Execute and renders directly), so it calls engine.WithTruncationNotice on every return path",
		carrierYes,
		"its format:\"json\" arm serves the standard browse envelope through engine.BrowseJSONResult, which the node row ceiling can clamp",
	},
	"intercept_query_explain_timeline.go:renderExplainWithNames": {
		disclosureHandles,
		"bypasses engine.Render, and its endpoint hydrate is a bulk ids[] read over both endpoints of every incident edge — a set the 50,000-row edge drain can push past the 10,000-id bound, which flags on the request alone; discloses via engine.WithTruncationNoticeFor",
		carrierNA,
		"renders the explain blocks as text",
	},
	"intercept_query_knowledge_search.go:composeRecentBrowse": {
		disclosureCannot,
		"a paging.DrainKeysetPages enumeration: every page carries an explicit Limit of paging.BrowsePageSize, below the server row ceiling, so clampRequestLimit never clamps",
		carrierNA,
		"delegates its render to the search renderers, which carry their own rows",
	},
	"intercept_query_linkage.go:routeLinkageClient": {
		disclosureCannot,
		"the only Execute in its own body is a by-id linkage read returning one row; its stats and list-graphs arms read the Stats RPC and the graph catalog, neither of which resultRowCount counts",
		carrierNA,
		"delegates its json arm to linkageStatsClient, whose payload is graph statistics rather than rows",
	},
	"intercept_query_plan_tree.go:renderPlanTreeJSON": {
		disclosureHandles,
		"the json and projected plan_tree envelope, split out of InterceptQueryPlanTree when the annotation read pushed that function over the statement gate. It ORs the traversal verdict it is handed with the annotation read's own, puts `truncated` on the ENVELOPE ROOT unconditionally, and appends the prose notice via render.AppendTruncationNotice — plus a SECOND, separate notice for a failed annotation read, because that notice's text names a row ceiling that did not engage and a `limit` remedy which on this arm is the subtree depth",
		carrierYes,
		"the recursive plan-tree payload; the key goes on the envelope root and never inside buildPlanTreeJSON, because truncation is a property of the READ and not of a node",
	},
	"intercept_query_plan_tree.go:InterceptQueryPlanTree": {
		disclosureHandles,
		"calls render.AppendTruncationNotice — the third declared disclosure helper, permitted because a tree has no pages to walk and plan_tree's `limit` IS the subtree depth, so its action clause deliberately differs from the shared sentence. This function now owns the TEXT path only; the json and projected envelope is renderPlanTreeJSON above. On the text path the verdict ORs in the per-section annotation read, and a FAILED annotation read gets its own notice rather than riding this one, because a row-ceiling message names a cause that did not occur and a remedy that would not address it",
		carrierYes,
		"the recursive plan-tree payload rides a traversal verdict bound at intercept_query_plan_tree.go:88; the key goes on the ENVELOPE ROOT, never inside buildPlanTreeJSON, because truncation is a property of the READ and not of a node",
	},
	"intercept_query_practice_browse.go:practiceBrowse": {
		disclosureHandles,
		"bypasses engine.Render (it issues its own Execute and renders through engine.RenderBrowse), so it calls engine.WithTruncationNotice on its return",
		carrierNA,
		"delegates the envelope to renderBrowseResponse through RenderBrowse; that row is the declared carrier",
	},
	"manage_migrate_embed_identity.go:handleMigrateEmbedIdentity": {
		disclosureCannot,
		"its own Execute is a MUTATION_KIND_SET_EMBED_IDENTITY mutation; the identity it reads first comes from the graph catalog via recordedIdentityFor, which resultRowCount does not count",
		carrierNA,
		"renders the migration transition as text",
	},

	// The three create-* interceptors render the tree they just created. They
	// reach the same disclosure helper through the render. qualifier; the
	// scanner records bare selector names, so it sees AppendTruncationNotice.
	"intercept_create_plan.go:renderCreatePlanText": {
		disclosureHandles,
		"binds the traversal verdict off render.AssembleSubtree and returns through render.AppendTruncationNotice — a create that renders a clamped tree would otherwise report the new plan as complete",
		carrierNA,
		"renders the created tree as text; the sibling json arm of create_plan returns a create outcome, not this function",
	},
	"intercept_create_research.go:InterceptCreateResearch": {
		disclosureHandles,
		"its text arm binds the traversal verdict off render.AssembleSubtree and returns through render.AppendTruncationNotice",
		carrierNo,
		"its json arm's envelope is a create outcome — id, name, question_ids, warnings — not a row-bounded read's payload",
	},
	"intercept_create_test_plan.go:InterceptCreateTestPlan": {
		disclosureHandles,
		"its text arm binds the traversal verdict off render.AssembleSubtree and returns through render.AppendTruncationNotice",
		carrierNo,
		"its json arm's envelope is a create outcome — id, name, step_ids, warnings — not a row-bounded read's payload",
	},

	// cmd/knowledge/internal/projects/render — the third census root. It hosts
	// the tree-rendering disclosure sentence (AppendTruncationNotice) and the
	// assemble arms that render subtrees. Every arm below binds the verdict
	// render.AssembleSubtree returns and appends the notice before returning;
	// the notice goes in the ARM because the arm is the only place its own
	// result and its own verdict are in scope together.
	"assemble.go:resolveAssembleByName": {
		disclosureCannot,
		"a paging.DrainKeysetPages enumeration: every page carries an explicit Limit of paging.BrowsePageSize, below the server row ceiling, so clampRequestLimit never clamps and the drain pages until exhaustion",
		carrierNA,
		"builds no envelope — it returns a resolved id, and its *kgtools.ToolResult is non-nil only on the error and ambiguous-match paths, which render as text",
	},
	"test_plan.go:assembleTestPlanNewRun": {
		disclosureCannot,
		"its only Execute is a mutate(create_batch) creating the run session's test_run nodes; a mutation returns no rows, so resultRowCount has nothing to clamp and no ceiling verdict exists",
		carrierNA,
		"appends the session footer to the caller's text builder",
	},
	"plan.go:assemblePlan": {
		disclosureHandles,
		"binds the traversal verdict off render.AssembleSubtree and returns through render.AppendTruncationNotice; a clamped traversal shortens the rendered phase and step tree. For a SECTIONED plan the verdict is the OR of that traversal and the per-section annotation read, whose bulk ids[] hydrate the server can clamp — a clamped hydrate drops annotation rows, which would otherwise render a plan under review as one carrying fewer reviewer notes than it has",
		carrierNA,
		"renders markdown text",
	},
	"section.go:assembleSection": {
		disclosureHandles,
		"binds the verdict off render.FetchSectionAnnotations and returns through render.AppendTruncationNotice; that read ends in ONE unbounded bulk ids[] hydrate over the annotation set, which the server clamps on the id-set bound — and a clamped hydrate DROPS annotation rows outright, so a section would report fewer reviewer annotations than it carries with nothing else in the render saying so",
		carrierNA,
		"renders markdown text",
	},
	"research.go:assembleResearch": {
		disclosureHandles,
		"binds the traversal verdict off render.AssembleSubtree and returns through render.AppendTruncationNotice; the question list it renders comes out of that same clamped traversal",
		carrierNA,
		"renders markdown text",
	},
	"test_plan.go:assembleTestPlan": {
		disclosureHandles,
		"binds the traversal verdict off render.AssembleSubtree and returns through render.AppendTruncationNotice on both the newRun and the enumerate arms; the steps and their runs both come out of that traversal",
		carrierNA,
		"renders markdown text",
	},
	"instruction.go:assembleInstruction": {
		disclosureHandles,
		"binds the traversal verdict off render.AssembleSubtree and returns through render.AppendTruncationNotice",
		carrierNA,
		"renders markdown text",
	},
	"ticket.go:assembleTicket": {
		disclosureHandles,
		"binds the traversal verdict off render.AssembleSubtree and returns through render.AppendTruncationNotice; a clamped traversal silently shortens the ticket's plan and research lists, which looks exactly like a small ticket",
		carrierNA,
		"renders markdown text",
	},
	"project_container.go:assembleProjectContainer": {
		disclosureHandles,
		"binds the traversal verdict off render.AssembleSubtree and returns through render.AppendTruncationNotice; a clamped traversal silently shortens the project's ticket list, which looks exactly like a small project",
		carrierNA,
		"renders markdown text",
	},
	// The three arms that render no contains tree. Their only verdict comes from
	// the bulk hydrate of the peers their sections name, which is why they
	// enroll in this phase rather than with the tree arms.
	"decision.go:assembleDecision": {
		disclosureHandles,
		"binds the verdict render.FetchNodesByIDs returns over its informed-by and supports peers and returns through render.AppendTruncationNotice; a clamped hydrate silently shortens both sections",
		carrierNA,
		"renders markdown text",
	},
	"fallback.go:assembleFallback": {
		disclosureHandles,
		"binds the verdict render.FetchNodesByIDs returns over every edge peer and returns through render.AppendTruncationNotice; a clamped hydrate turns resolved peers into the shorter raw-id lines, which is otherwise indistinguishable from genuinely unresolvable peers",
		carrierNA,
		"renders markdown text",
	},
	"pattern.go:assemblePatternIn": {
		disclosureHandles,
		"binds the verdict render.FetchNodesByIDsIn returns over the pattern's children in its own practice graph and returns through render.AppendTruncationNotice; a clamped hydrate empties sections, and an emptied section here reads as a thin pattern",
		carrierNA,
		"renders markdown text",
	},
	"json.go:assembleJSON": {
		disclosureHandles,
		"binds the traversal verdict off render.AssembleSubtree, ORs in the linked-node hydrate's and the per-section annotation read's, and returns through render.AppendTruncationNotice alongside the envelope key below — the key is what a machine reads, the block is what a caller reads. The annotation term is the one this arm dropped: its verdict was discarded with a blank identifier, so a clamped annotation hydrate emitted `truncated: false`, which on an unconditional key is an affirmative statement that a plan under review carries no review state",
		carrierYes,
		"the recursive assemble tree is a row-bounded read's payload; the LIVE verdict rides the ENVELOPE ROOT, never a per-row key, because truncation is a property of the READ and not of a node",
	},
}
