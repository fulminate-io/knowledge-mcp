// SPDX-License-Identifier: Apache-2.0

// collect_recipe_land.go — the LANDING arm of a recipe collect: the refusals
// that run before anything, the target-graph read the whole design rests on, and
// the one create_batch that writes the emitted set into the combined practice
// graph.
//
// WHY THE LANDING LIVES HERE AND NOT IN THE RECIPE PACKAGE. The write goes
// through the MUTATE route, whose client seam (executeMutate) is package-private
// to this package, and the route matters: it is the one that runs the server's
// summary-and-name validator on every created body, so a landed practice node
// cannot be summaryless. The recipe package therefore composes and this package
// decides — which also puts every refusal below AHEAD of the source read, not
// merely ahead of the write.
//
// THE ORDER OF THE REFUSALS IS THE SAFETY PROPERTY. recipeRunOptions refuses the
// render parameters before anything at all; then the target graph is probed and
// the raw graph's origin read, both before recipe.RunRecipe is called; then the
// collision read runs and its truncation verdict is treated as fatal; only then
// is a single create_batch composed and executed. A refusal reached after the
// write is a refusal that already wrote.

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	"github.com/fulminate-io/knowledge-mcp/internal/recipe"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	"github.com/fulminate-io/knowledge-mcp/internal/workingset"
)

const (
	// landingOriginMetaKey records, ON THE HUB, the raw graph's own recorded
	// source identity — the pdf's path or the web crawl's seed host. It answers
	// "where did this collection come from" after the raw graph itself has been
	// dropped, which is the ordinary end state: raw graphs are scratch.
	//
	// It is NOT the hub's `kind`, which says which FAMILY the origin is, and not
	// the node's top-level `source` field, which is the emitter's own stamp.
	landingOriginMetaKey = "origin"

	// landingVersionMetaKey carries a landed node's integer version as a decimal
	// string. Absent means version 1: the first landing of an id writes no key at
	// all, so the corpus is not rewritten to carry a value that means "original".
	//
	// IT MUST LIVE ON THE NODE rather than on the version edge's Evidence, and
	// that is a mechanical constraint. The next landing has to READ the current
	// version to increment it, and it reads through the same by-ids bulk hydrate
	// it already issues; an edge's Evidence is reachable only by walking the edge,
	// which is a second read of a different shape for a value the first read is
	// already carrying the node for.
	landingVersionMetaKey = "version"
)

// landingTarget is the graph a landing writes into and hashes its ids under.
//
// ONE FUNCTION RATHER THAN A LITERAL AT EACH SITE, because three separate things
// must name the SAME key or the landing silently misbehaves: the target the
// emitted ids hash under, the graph the collision read is issued against, and the
// graph the create_batch is routed to. A second spelling of any one of them turns
// every collision lookup into a miss, and a miss on this path is an overwrite.
//
// The NAME is resolved through workingset rather than written out: practice is a
// singleton family, and the canonical instance name for a singleton is the one
// place that fact is recorded.
func landingTarget() recipe.TargetSpec {
	return recipe.TargetSpec{
		GraphType: kgtypes.GraphPractice,
		Name:      workingset.CanonicalInstanceName(kgtypes.GraphPractice, ""),
	}
}

// landingTargetKey renders the landing target as the "<type>/<name>" string
// StableID takes as its first component.
func landingTargetKey() string { return recipe.TargetKey(landingTarget()) }

// landingWireName is the instance name every landing READ and WRITE puts on the
// WIRE SELECTOR, and it is deliberately EMPTY while landingTarget().Name is
// "default". The two are different names for the same graph and they are not
// interchangeable.
//
// THE HASH KEY IS THE CANONICAL INSTANCE NAME. StableID's first component has to
// be a stable string, and the canonical name is what this process keys the graph
// under internally, so "practice/default" is what an emitted id hashes under.
//
// THE WIRE NAME IS EMPTY BECAUSE PRACTICE IS A SINGLETON. Its selector policy
// carries no instance field at all, and foundation's graphTarget reads a NON-EMPTY
// practice name as the LEGACY per-language selector — it sets Language, which
// addresses one of the eight pre-singleton graphs rather than the combined one.
// So passing the canonical name onto the wire silently redirects every read to a
// graph that holds none of this corpus: the collision read finds no resident for
// any emitted id, the twin branch never fires, and the add-not-upsert write lands
// on top of whatever a human had edited. Nothing errors.
//
// This is measured rather than reasoned: the first revision of this file passed
// landingTarget().Name to both foundation calls, and the by-selector assertions in
// collect_recipe_land_test.go caught it as a `language=default` on the read.
const landingWireName = ""

// landingPreflight is what the pre-run probes established: which hub this
// landing groups under, whether that hub already exists, and the two facts a new
// hub needs to be created with.
type landingPreflight struct {
	hubID string
	// hubExists distinguishes "reuse it" from "create it". A landing that
	// created a hub unconditionally would mint one per run and leave every run's
	// members pointing at a different id.
	hubExists bool
	kind      string
	origin    string
	slug      string
}

// preflightLanding runs every refusal that can be decided BEFORE the source
// graph is read, and returns the hub facts the composer needs.
//
// TWO PROBES, IN THIS ORDER, and the order is deliberate. The target graph is
// probed first because "there is nowhere to write this" is the refusal a caller
// most needs to hear first and the one that makes every later step pointless;
// the raw graph's origin is read second because it is a fact about the SOURCE
// that only matters once a destination exists.
//
// THE TARGET PROBE AND THE HUB PROBE ARE ONE READ. A by-type browse of the
// combined graph narrowed to this hub's id answers both: an absent GRAPH fails
// the read with not-found, and an absent HUB inside a present graph returns zero
// rows. Splitting them would pay two round trips for one question.
func preflightLanding(ctx context.Context, deps ClientDeps, a collectArgs) (*landingPreflight, error) {
	slug := a.ID
	pre := &landingPreflight{
		hubID: recipe.StableID(landingTargetKey(), slug, string(kgtypes.NodeSource), slug),
		kind:  a.Type,
		slug:  slug,
	}

	hubs, err := foundation.FetchNodesByTypeMeta(ctx, deps.GraphCaller(),
		landingTarget().GraphType, landingWireName, kgtypes.NodeSource,
		map[string]string{kgtypes.MetaKeySourceHub: pre.hubID})
	if err != nil {
		return nil, landingTargetReadError(err)
	}
	pre.hubExists = len(hubs) > 0

	// THE ORIGIN READ HAS THREE OUTCOMES AND ALL THREE ARE REFUSALS HERE.
	// rawGraphRecordedSource errors on a nil caller and on a failed root read, and
	// returns ("", nil) when the graph holds no root or its root carries no
	// recorded source key. The collect path treats that third state as admissible
	// — an unrecorded source is not a different source — but a LANDING is not a
	// re-collect: it stamps the origin onto a hub that outlives the raw graph, so
	// an empty origin would create a hub that permanently claims to know nothing
	// about where its contents came from.
	key := rawSourceMetaKey(a.Type)
	origin, oerr := rawGraphRecordedSource(ctx, deps, a.Type, slug, key)
	if oerr != nil {
		return nil, fmt.Errorf(
			"collect %s transformer=recipe land: the raw graph %q could not be read for its recorded source, "+
				"so the hub would be created with an origin nobody verified: %w", a.Type, slug, oerr)
	}
	if origin == "" {
		return nil, fmt.Errorf(
			"collect %s transformer=recipe land: the raw graph %q records no source under %q on its root node — "+
				"it was collected before the %s collector began stamping one, and a landing writes that value onto a "+
				"hub that outlives the raw graph. Re-collect the document (collect type=%s id=…) so its root carries "+
				"the key, then land again. Nothing was written and no default hub was created",
			a.Type, slug, key, a.Type, a.Type)
	}
	pre.origin = origin
	return pre, nil
}

// landingTargetReadError classifies the target-graph probe's failure into the
// two refusals requirement 8 distinguishes.
//
// A NOT-FOUND IS THE ABSENT GRAPH, and it is a distinct answer rather than a
// flavor of read failure: a practice graph comes into existence on its first
// node-materializing write, so before anything has ever been written to it a read
// against it is not-found. The landing refuses rather than creating it, because a
// landing that brought the corpus into existence would make a typo'd first run
// indistinguishable from a deliberate one.
func landingTargetReadError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Code() == connect.CodeNotFound {
		return fmt.Errorf(
			"collect transformer=recipe land: the combined practice graph does not exist yet, so there is nothing " +
				"to land into. It is created by the first write to it — author one practice node by hand " +
				"(mutate operation=create graph=practice type=pattern …) and land again. Nothing was written")
	}
	return fmt.Errorf(
		"collect transformer=recipe land: the combined practice graph could not be read, so a landing cannot tell a "+
			"resident node from an absent one and would overwrite whatever it failed to see: %w", err)
}

// landingBatchArgs is the create_batch envelope a landing sends. It is
// persistBatchArgs plus the graph selector: PersistBatch's own envelope carries
// no graph because every one of its callers writes to the knowledge graph, and
// widening that struct would put a practice-only field on every knowledge write.
type landingBatchArgs struct {
	Operation string             `json:"operation"`
	Graph     string             `json:"graph"`
	Nodes     []persistBatchNode `json:"nodes"`
	Edges     []persistBatchEdge `json:"edges"`
}

// landingOutcome is what the composer decided, for the renderer to disclose.
type landingOutcome struct {
	hubID      string
	hubCreated bool
	landed     int
	matched    int
	twins      []landingTwin
}

// landingTwin records one collision resolved into a versioned twin.
type landingTwin struct {
	name    string
	id      string
	version int
}

// composeLanding turns the emitted set plus the resident map into the ONE
// create_batch a landing writes, and reports what it decided.
//
// EVERY EMITTED NODE TAKES EXACTLY ONE OF TWO ROUTES and there is no third. An id
// with no resident lands as a BASE node under its own id. An id with a resident —
// machine-landed, hand-edited or soft-deleted alike — lands as a TWIN under a
// derived id with the next version, and the resident is not in the batch at all.
// That is what makes requirement 7 structural rather than a check: the resident's
// id is never written, so there is no path on which it can be overwritten.
//
// THE HUB IS IN THE SAME BATCH AS ITS MEMBERS. A follow-up call would leave a
// window in which members exist under a hub that does not, which is invisible to
// every hub-scoped read and is precisely the state a failed collection needs to
// be deletable from.
//
// THE BODY'S OWN link EDGES RIDE THE SAME BATCH TOO, re-pointed onto the slots
// the nodes landed in. A composer that shipped only the hub and version edges
// would silently discard every relation the recipe author wrote, and the author
// would read a successful landing whose graph has no structure in it.
func composeLanding(
	pre *landingPreflight, nodes []*knowledgev1.Node, edges []kgwire.BatchEdge,
	chains map[string]landingResidency,
) (landingBatchArgs, landingOutcome, error) {
	out := landingOutcome{hubID: pre.hubID, hubCreated: !pre.hubExists, matched: len(nodes)}
	args := landingBatchArgs{Operation: "create_batch", Graph: string(kgtypes.GraphPractice)}

	if !pre.hubExists {
		args.Nodes = append(args.Nodes, hubBody(pre))
	}

	// slotOf maps an EMITTED id onto the batch slot the node actually landed in,
	// which is not the same thing as its id: a collided id lands under a twin's
	// id, and a link rule naming the emitted id must follow the node rather than
	// point at the resident it was versioned away from.
	slotOf := make(map[string]int, len(nodes))

	for _, n := range nodes {
		body := persistBatchNode{
			Type:        n.GetType(),
			Name:        n.GetSymbolName(),
			Description: n.GetDescription(),
			Summary:     n.GetSummary(),
			Content:     n.GetContent(),
			Status:      n.GetStatus(),
			Metadata:    landingMetadata(n, pre.hubID),
			ID:          n.GetId(),
			Source:      n.GetSource(),
		}
		resident, collided := chains[n.GetId()]
		if collided {
			version := resident.latestVersion + 1
			body.ID = recipe.VersionedTwinID(landingTargetKey(), pre.slug, n.GetType(), n.GetId(), version)
			body.Metadata[landingVersionMetaKey] = strconv.Itoa(version)
			out.twins = append(out.twins, landingTwin{name: n.GetSymbolName(), id: body.ID, version: version})
		}
		slot := len(args.Nodes)
		slotOf[n.GetId()] = slot
		args.Nodes = append(args.Nodes, body)
		// The hub edge: node → hub, one per landed node, by slot on the node end
		// because the node is being created in this same batch.
		args.Edges = append(args.Edges, persistBatchEdge{
			FromIdx: slot, ToIdx: -1, ToID: pre.hubID, Type: string(kgtypes.EdgeSourcedFrom),
		})
		if collided {
			// The version edge: the PREVIOUS LATEST → the new twin. The old end is
			// an existing node addressed by id, and it is the chain's latest rather
			// than its base: a v3 twin hangs off v2, so the chain reads as a chain
			// instead of a fan of edges out of the original.
			args.Edges = append(args.Edges, persistBatchEdge{
				FromIdx: -1, FromID: resident.latestID, ToIdx: slot, Type: string(kgtypes.EdgeNextVersion),
			})
		}
		out.landed++
	}

	linked, lerr := landingLinkEdges(edges, slotOf)
	if lerr != nil {
		return landingBatchArgs{}, landingOutcome{}, lerr
	}
	args.Edges = append(args.Edges, linked...)
	return args, out, nil
}

// landingLinkEdges re-points the body's own link edges onto batch slots.
//
// AN ENDPOINT THIS LANDING DID NOT EMIT IS A REFUSAL, not a dropped edge. Every
// edge a recipe body produces has both endpoints verified against the in-run
// emitted set, so an unmappable one means something built an edge outside the
// link rules — the retired provenance edge into the raw graph being the exact
// case — and shipping it would write into a graph this landing does not own
// while dropping it would silently discard a value the run produced. Neither is
// admissible, so the run is refused naming the edge.
func landingLinkEdges(edges []kgwire.BatchEdge, slotOf map[string]int) ([]persistBatchEdge, error) {
	out := make([]persistBatchEdge, 0, len(edges))
	for _, e := range edges {
		from, fromOK := slotOf[e.FromID]
		to, toOK := slotOf[e.ToID]
		if !fromOK || !toOK {
			return nil, fmt.Errorf(
				"collect transformer=recipe land: the run produced a %q edge from %q to %q, and %s is not a node this "+
					"landing emitted — a landing writes only into the combined practice graph, so an edge reaching "+
					"outside the emitted set is neither written nor silently dropped. Nothing was written",
				e.Type, e.FromID, e.ToID, unmappableEndpoint(e, fromOK))
		}
		out = append(out, persistBatchEdge{
			FromIdx: from, ToIdx: to, Type: string(e.Type),
			Weight: e.Weight, Confidence: e.Confidence, Method: e.Method, Evidence: e.Evidence,
		})
	}
	return out, nil
}

// unmappableEndpoint names which end of an edge the landing could not place, so
// the refusal points at one id rather than at both.
func unmappableEndpoint(e kgwire.BatchEdge, fromOK bool) string {
	if !fromOK {
		return "the source " + e.FromID
	}
	return "the target " + e.ToID
}

// hubBody builds the `source` hub node a first landing creates.
//
// IT CARRIES ITS OWN ID UNDER THE HUB KEY, which reads like a redundancy and is
// not: the by-hub delete selects on that metadata predicate, so a hub that did
// not carry its own id would survive the delete of everything it groups and be
// left behind as an empty hub nothing points at. Requirement 6 says the hub goes
// with its members, and this is what makes the existing predicate do it.
//
// THE SUMMARY IS AUTHOR-SUPPLIED because a `source` node is never
// auto-summarized: the server's create validator refuses a body of that type
// without one, which is the gate the mutate route exists to run.
func hubBody(pre *landingPreflight) persistBatchNode {
	return persistBatchNode{
		Type: string(kgtypes.NodeSource),
		Name: pre.slug,
		Summary: fmt.Sprintf("%s source hub for the %s collection %q, grouping every practice node landed from it",
			pre.kind, pre.kind, pre.slug),
		Description: fmt.Sprintf(
			"The hub every practice node landed from the %s raw graph %q is grouped under. Its members carry this "+
				"hub's id under the %s metadata key and one %s edge to it; deleting the hub deletes the collection.",
			pre.kind, pre.slug, kgtypes.MetaKeySourceHub, kgtypes.EdgeSourcedFrom),
		Metadata: map[string]string{
			kgtypes.MetaKeySourceHub:     pre.hubID,
			kgtypes.MetaKeySourceHubKind: pre.kind,
			landingOriginMetaKey:         pre.origin,
		},
		ID:     pre.hubID,
		Source: "recipe:" + pre.slug,
	}
}

// landingMetadata copies an emitted node's metadata and folds in the hub key.
//
// IT COPIES rather than mutating the emitted node, because the same Result is
// also what an extract render would read; a composer that stamped in place would
// make the two arms disagree about what the emitter produced.
func landingMetadata(n *knowledgev1.Node, hubID string) map[string]string {
	// SIZED FROM ONE LENGTH, NOT A SUM: a make() capacity is a hint —
	// the two folded-in keys cost at most a growth — while a size built by
	// addition is a value the allocator has to take on trust.
	out := make(map[string]string, len(n.GetMetadata()))
	maps.Copy(out, n.GetMetadata())
	out[kgtypes.MetaKeySourceHub] = hubID
	return out
}

// landRecipeRun performs the landing: the collision read, the composition and
// the one create_batch. The preflight has already run.
func landRecipeRun(
	ctx context.Context, deps ClientDeps, a collectArgs, pre *landingPreflight, res *recipe.Result,
) (landingOutcome, error) {
	// THE RESIDENT READ IS A CHAIN WALK, not a single lookup of the emitted ids.
	// A twin is stored under a DERIVED id, so a read of the emitted ids alone
	// would see version 1 forever and every re-run after the second would re-mint
	// the SAME v2 id over the twin the run before it wrote. See
	// collect_recipe_land_version.go for the walk and for why it probes derived
	// ids rather than traversing next-version edges.
	chains, err := landingVersionChains(ctx, deps, a, pre, res.Nodes)
	if err != nil {
		return landingOutcome{}, err
	}

	args, out, cerr := composeLanding(pre, res.Nodes, res.Edges, chains)
	if cerr != nil {
		return landingOutcome{}, cerr
	}
	if len(args.Nodes) == 0 {
		// Nothing matched and the hub already exists: there is no batch to send.
		// Reported by the renderer rather than sent as an empty write.
		return out, nil
	}
	payload, merr := json.Marshal(args)
	if merr != nil {
		return landingOutcome{}, fmt.Errorf("collect %s transformer=recipe land: marshal create_batch: %w", a.Type, merr)
	}
	if _, eerr := executeMutate(ctx, deps.GraphCaller(), payload); eerr != nil {
		return landingOutcome{}, fmt.Errorf("collect %s transformer=recipe land: %w", a.Type, eerr)
	}
	return out, nil
}
