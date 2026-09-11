// SPDX-License-Identifier: Apache-2.0

// practice_hub_guard.go — the practice hub rules, at the position every caller
// passes through.
//
// WHY THEY MOVED HERE. Every hub rule this branch built lived in
// InterceptMutate, and a route table derived from the tree found two callers
// that never reach it: the recipe landing sends its create_batch straight to
// engine.Compile through the tools' own executeMutate funnel, and the standalone
// `delete` tool reaches engine.Dispatch. Both bypassed every rule. A gate added
// per bypassing caller is a gate the next caller bypasses, so the rules run
// BENEATH all three.
//
// THE TOOLS-LAYER GATES STAY for their PLACE: they run above the cross-graph link
// composer and above the dispatch tree, where a caller gets a message naming the
// arm and the payload path it typed, and the off-family one answers before a
// graph caller is even resolved. What they must not be is the SOLE position,
// which is what this file fixes.
//
// THE REFUSAL TEXT IS THIS PACKAGE'S, rendered there: the create body rule, the
// membership edge and the off-family rule all call the constructors below, so no
// two positions can tell a caller different things about one rule. The engine's
// own off-family arm reads BOTH hub spellings — `source_hub` on the mutate arms
// and `source` on the standalone delete tool — where the tools-layer one reads
// only the spelling a mutate payload can carry.
//
// A HUB CARRIES ITS OWN ID UNDER THE HUB KEY, and that is the contract, not
// nesting. The recipe landing and the migration driver both write it on purpose:
// the by-hub delete selects on that one metadata predicate, so a self-keyed hub
// is swept together with its members in ONE write. A `source` node keyed to some
// OTHER node is the shape that is refused — that would make the hub a node
// belongs to a chain rather than a value.
//
// A PAYLOAD MAY NAME THE HUB IT IS CREATING. A create_batch that lands a hub and
// its members together names an id no read can resolve yet, because the batch is
// what makes it exist. Those ids are admitted from the payload rather than read.

package engine

import (
	"context"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// practiceBodyArg is one body a practice write payload carries.
type practiceBodyArg struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Metadata map[string]string `json:"metadata"`
}

// practiceEdgeArg is one edge a create_batch carries, in the batch's own slot
// form. The hub end may be a slot index or a literal id.
type practiceEdgeArg struct {
	FromIdx int    `json:"from_idx"`
	ToIdx   int    `json:"to_idx"`
	ToID    string `json:"to_id"`
	Type    string `json:"type"`
}

// practiceWriteArgs is the payload shape these rules decide on. It is a decode of
// its own rather than a reuse of mutateArgs because the delete tool's `source`
// and the batch bodies live in three different arg structs, and this gate reads
// one shape for all of them.
type practiceWriteArgs struct {
	Operation string            `json:"operation"`
	Graph     string            `json:"graph"`
	SourceHub string            `json:"source_hub"`
	Source    string            `json:"source"`
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Metadata  map[string]string `json:"metadata"`
	Nodes     []practiceBodyArg `json:"nodes"`
	Items     []practiceBodyArg `json:"items"`
	Updates   []practiceBodyArg `json:"updates"`
	Edges     []practiceEdgeArg `json:"edges"`
}

// PracticeHubOffFamilyRefusal renders the refusal for a hub named on a family
// that has no hubs. Exported so the tools-layer gate renders the same sentence.
func PracticeHubOffFamilyRefusal(param, hub, graph, operation string) error {
	family, addressed := graph, fmt.Sprintf("graph:%q", graph)
	if graph == "" {
		family = string(kgtypes.GraphKnowledge)
		addressed = "the knowledge family, which is what an omitted `graph` addresses"
	}
	return fmt.Errorf(
		"`%s`=%q is a PRACTICE-FAMILY parameter: it names a source hub in the combined practice graph. This "+
			"call addresses %s, and %q has no source hubs for it to name, so on a %s it can only reach "+
			"nothing — it is refused rather than dropped. Drop `%s`, or address the combined practice graph "+
			"with graph:\"practice\"",
		param, hub, addressed, family, operation, param)
}

// PracticeHubNotAHubRefusal renders the refusal for an id that does not name a
// live hub. The three failures are three sentences: absent, deleted, and a node
// of another type send the caller to three different fixes.
func PracticeHubNotAHubRefusal(param, hub string, node *knowledgev1.Node) error {
	switch {
	case node == nil || node.GetId() == "":
		return fmt.Errorf(
			"`%s`=%q resolves to no node in the combined practice graph at all, so it is not a hub — a write "+
				"under it would carry the id in the node's %s metadata AND draw a %s edge to nothing, leaving "+
				"the node filed under a hub no traverse can reach. Create the hub first (a %q node), or name "+
				"an existing one; nothing was written",
			param, hub, kgtypes.MetaKeySourceHub, kgtypes.EdgeSourcedFrom, kgtypes.NodeSource)
	case node.GetTombstonedAt() != 0:
		return fmt.Errorf(
			"`%s`=%q names a hub that has been deleted, so a write under it would join a collection that is "+
				"gone. Name a live hub, or create this one again; nothing was written",
			param, hub)
	case node.GetType() != string(kgtypes.NodeSource):
		return fmt.Errorf(
			"`%s`=%q is a `%s` node, not a %q node, so it is not a hub — a practice node is grouped under a "+
				"hub, never under another member. Name the hub this node is itself grouped under, or create "+
				"one; nothing was written",
			param, hub, node.GetType(), kgtypes.NodeSource)
	}
	// THE SELF-KEY IS THE CONTRACT, not nesting: a hub carries its OWN id under
	// the hub key so the by-hub delete's single metadata predicate sweeps the hub
	// with its members. Only a key naming some OTHER node is a chain.
	if keyed := node.GetMetadata()[kgtypes.MetaKeySourceHub]; keyed != "" && keyed != node.GetId() {
		return fmt.Errorf(
			"`%s`=%q names a %q node that is itself grouped under %q, and a hub is grouped under nothing but "+
				"itself. A hub carries its OWN id under the %s key, which is what lets one predicate sweep the "+
				"hub with its members; a key naming another node makes the hub a node belongs to a chain "+
				"rather than a value. Name a hub of its own; nothing was written",
			param, hub, kgtypes.NodeSource, keyed, kgtypes.MetaKeySourceHub)
	}
	return nil
}

// PracticeHubNestedCreateRefusal renders the refusal for a create that would
// write a `source` node keyed to some other node.
func PracticeHubNestedCreateRefusal(role, bodyID, keyed string) error {
	return fmt.Errorf(
		"`%s` creates a %q node keyed to %q under `%s`, and a hub is grouped under nothing but itself: a hub "+
			"carries its OWN id there (%q would be the value), which is what lets one predicate sweep the hub "+
			"with its members. Nothing was written",
		role, kgtypes.NodeSource, keyed, kgtypes.MetaKeySourceHub, bodyID)
}

// PracticeHubBodyWithoutEdgeRefusal renders the refusal for a body that names a
// hub in its metadata while the payload draws no membership edge for it.
func PracticeHubBodyWithoutEdgeRefusal(role, hub string) error {
	return fmt.Errorf(
		"`%s`=%q writes the %s metadata key with no %s edge beside it, so the node would be filed under a hub "+
			"nothing links it to. Group it with the call's `%s` parameter, which writes the key and the edge "+
			"together, or carry the edge in the same batch; nothing was written",
		role, hub, kgtypes.MetaKeySourceHub, kgtypes.EdgeSourcedFrom, kgtypes.MetaKeySourceHub)
}

// GuardPracticePayload is the PAYLOAD-DECIDABLE subset of the hub rules, the part
// that needs no read. The tools-layer gate calls it so the two positions run ONE
// implementation: a rule stated twice is a rule that drifts, and the version that
// drifted here refused the self-keyed hub the shipped landing writes.
//
// IT DOES NOT NAME THE ARM, and that is the difference between it and
// GuardPracticeWrite beside it. This one is called from INSIDE a tools-layer gate
// whose own call site already prefixes the refusal with the arm, so naming the arm
// here produced "mutate(create): mutate(create): …" — one message framed twice
// because two layers each believed the framing was theirs. The terminal position
// keeps the prefix; the delegated one leaves it to the caller that has it.
func GuardPracticePayload(args json.RawMessage) error {
	var a practiceWriteArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil //nolint:nilerr // a payload that does not parse is the dispatcher's error to report
	}
	if err := practiceHubOffFamily(a); err != nil {
		return err
	}
	if a.Graph != string(kgtypes.GraphPractice) {
		return nil
	}
	if err := practiceHubBodiesCarryTheirEdge(a); err != nil {
		return err
	}
	// The closed-vocabulary rule is deliberately NOT called here; practice_type_guard.go
	// records the measurement that put it at the terminal position only.
	return practiceHubNestedCreate(a)
}

// GuardPracticeWrite is the engine-layer position of the hub rules. Every write
// that can reach a practice graph runs it: the mutate tool through Dispatch, the
// standalone delete tool through Dispatch, and the direct compile-and-execute
// callers (the recipe landing, the style-rule import, the checks writer) through
// the tools' executeMutate funnel.
//
// IT RETURNS nil FOR EVERY NON-PRACTICE PAYLOAD before reading anything, so no
// other family pays for it.
func GuardPracticeWrite(ctx context.Context, exec ExecuteFn, args json.RawMessage) error {
	var a practiceWriteArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil //nolint:nilerr // a payload that does not parse is the dispatcher's error to report
	}
	return practiceHubArm(a, guardPracticeWriteRules(ctx, exec, a))
}

// practiceHubArm prefixes a refusal with the arm that earned it. A caller reads
// the message next to the call it made, and "which arm" is the first thing it
// needs — the tools-layer positions have always said it, and a rule that moved
// beneath them must not lose it.
func practiceHubArm(a practiceWriteArgs, err error) error {
	if err == nil {
		return nil
	}
	if a.Operation == "" {
		return fmt.Errorf("%s: %w", practiceHubArmLabel(a), err)
	}
	return fmt.Errorf("mutate(%s): %w", a.Operation, err)
}

// guardPracticeWriteRules is the rule sequence itself: the payload-decidable
// refusals first, then the one that reads.
func guardPracticeWriteRules(ctx context.Context, exec ExecuteFn, a practiceWriteArgs) error {
	if err := practiceHubOffFamily(a); err != nil {
		return err
	}
	if a.Graph != string(kgtypes.GraphPractice) {
		return nil
	}
	if err := practiceHubBodiesCarryTheirEdge(a); err != nil {
		return err
	}
	if err := practiceHubNestedCreate(a); err != nil {
		return err
	}
	// The VOCABULARY rule is payload-decidable and belongs with the rules above
	// the one that reads: a body whose type the graph does not enroll is refused
	// without resolving anything, so no read is spent on a write that cannot land.
	if err := practiceCreateTypes(a); err != nil {
		return err
	}
	return practiceHubsResolve(ctx, exec, a)
}

// practiceHubOffFamily is the payload-decidable half: a hub parameter on any
// family but practice reaches nothing, and bad input errors.
func practiceHubOffFamily(a practiceWriteArgs) error {
	if a.Graph == string(kgtypes.GraphPractice) {
		return nil
	}
	// BOTH SPELLINGS, and reading only one is what left this gate unreachable for
	// the one the delete tool publishes. The mutate arms spell the hub selector
	// `source_hub`; the standalone `delete` tool spells it `source` and REJECTS
	// `source_hub` at its schema, so a family check that read `source_hub` alone
	// could never fire on that tool — and a delete naming another family compiled
	// a real metadata-predicate DELETE against it.
	//
	// `source` COUNTS ONLY WHERE IT IS THE HUB AXIS. On the mutate arms it is the
	// node's PROVENANCE field and means something else entirely, which is why the
	// delete tool is identified by its empty operation rather than by the presence
	// of the key.
	param, hub := "source_hub", a.SourceHub
	if hub == "" && a.Operation == "" {
		param, hub = "source", a.Source
	}
	if hub == "" {
		return nil
	}
	return PracticeHubOffFamilyRefusal(param, hub, a.Graph, practiceHubArmLabel(a))
}

// practiceHubArmLabel names the arm a refusal came from, and never renders blank.
// The standalone delete tool carries no `operation`, and a sentence reading "on a
//
//	it can only reach nothing" names nothing at all.
func practiceHubArmLabel(a practiceWriteArgs) string {
	if a.Operation == "" {
		return "delete"
	}
	return a.Operation
}

// practiceHubBodies returns every body a payload carries, with the payload path
// that named it.
func practiceHubBodies(a practiceWriteArgs) map[string]practiceBodyArg {
	// SIZED FROM ONE LENGTH, NOT A SUM: a make() capacity is a hint —
	// the items, updates and metadata bodies cost at most a growth — while a
	// size built by addition is a value the allocator has to take on trust.
	out := make(map[string]practiceBodyArg, len(a.Nodes))
	if len(a.Metadata) > 0 || a.Type != "" {
		out["metadata"] = practiceBodyArg{ID: a.ID, Type: a.Type, Metadata: a.Metadata}
	}
	for i, b := range a.Nodes {
		out[fmt.Sprintf("nodes[%d]", i)] = b
	}
	for i, b := range a.Items {
		out[fmt.Sprintf("items[%d]", i)] = b
	}
	for i, b := range a.Updates {
		out[fmt.Sprintf("updates[%d]", i)] = b
	}
	return out
}

// practiceHubNestedCreate refuses a create that writes a `source` node keyed to
// some other node. A body keyed to its OWN id is the hub contract and passes.
func practiceHubNestedCreate(a practiceWriteArgs) error {
	if a.Operation != "create" && a.Operation != "create_batch" {
		return nil
	}
	for role, b := range practiceHubBodies(a) {
		if b.Type != string(kgtypes.NodeSource) {
			continue
		}
		keyed := b.Metadata[kgtypes.MetaKeySourceHub]
		if keyed == "" || keyed == b.ID {
			continue
		}
		return PracticeHubNestedCreateRefusal(role, b.ID, keyed)
	}
	return nil
}

// practiceHubBodiesCarryTheirEdge refuses a body that names a hub in its metadata
// on a call that names none, UNLESS the payload draws that body's membership edge
// itself.
//
// THE EDGE IS WHAT MAKES A BODY KEY LEGAL. Grouping is the key AND the edge; the
// call's `source_hub` parameter emits both, and a batch that carries its own
// `sourced-from` edges — which is exactly what the recipe landing composes —
// emits both too. A body key with neither is half a write.
//
// A HUB CARRIES NO EDGE AND NEEDS NONE, whatever its key says: it is the hub, and
// the key is what makes the by-hub delete sweep it. A `source` body is therefore
// skipped here entirely and judged by the nesting rule instead, which is the one
// that knows a hub may be keyed only to itself — this gate firing first on such a
// body reported the wrong fault, naming an absent edge rather than the hub the
// caller actually typed.
func practiceHubBodiesCarryTheirEdge(a practiceWriteArgs) error {
	if a.SourceHub != "" || (a.Operation != "create" && a.Operation != "create_batch") {
		return nil
	}
	edged := practiceHubEdgedSlots(a)
	for i, b := range a.Nodes {
		hub := b.Metadata[kgtypes.MetaKeySourceHub]
		if hub == "" || hub == b.ID || edged[i] == hub || b.Type == string(kgtypes.NodeSource) {
			continue
		}
		return PracticeHubBodyWithoutEdgeRefusal(fmt.Sprintf("nodes[%d].metadata.%s", i, kgtypes.MetaKeySourceHub), hub)
	}
	if hub := a.Metadata[kgtypes.MetaKeySourceHub]; hub != "" && hub != a.ID &&
		a.Type != string(kgtypes.NodeSource) {
		return PracticeHubBodyWithoutEdgeRefusal("metadata."+kgtypes.MetaKeySourceHub, hub)
	}
	return nil
}

// practiceHubEdgedSlots maps each batch slot to the hub its own `sourced-from`
// edge names, when the payload draws one.
func practiceHubEdgedSlots(a practiceWriteArgs) map[int]string {
	out := make(map[int]string, len(a.Edges))
	for _, e := range a.Edges {
		if e.Type != string(kgtypes.EdgeSourcedFrom) {
			continue
		}
		to := e.ToID
		if to == "" && e.ToIdx >= 0 && e.ToIdx < len(a.Nodes) {
			to = a.Nodes[e.ToIdx].ID
		}
		out[e.FromIdx] = to
	}
	return out
}

// practiceHubsResolve reads every hub the payload NAMES and holds it to the hub
// contract. Ids the payload itself creates are admitted from the payload: the
// batch is what makes them exist, so no read can resolve them yet.
func practiceHubsResolve(ctx context.Context, exec ExecuteFn, a practiceWriteArgs) error {
	created := practiceHubCreatedIDs(a)
	named := practiceHubNamedIDs(a)
	pending := make([]string, 0, len(named))
	for _, n := range named {
		if !created[n.hub] {
			pending = append(pending, n.hub)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	nodes, err := practiceNodesByIDs(ctx, exec, pending)
	if err != nil {
		return fmt.Errorf(
			"`%s` names a source hub in the combined practice graph, and that graph could not be read to "+
				"check it; nothing was written: %w", kgtypes.MetaKeySourceHub, err)
	}
	for _, n := range named {
		if created[n.hub] {
			continue
		}
		if rerr := PracticeHubNotAHubRefusal(n.param, n.hub, nodes[n.hub]); rerr != nil {
			return rerr
		}
	}
	return nil
}

// practiceHubNamed is one hub a payload names and the payload path that named it.
type practiceHubNamed struct{ param, hub string }

// practiceHubNamedIDs collects every hub a payload names, deduped, first path
// winning. The delete tool's `source` is the same axis under its own spelling.
func practiceHubNamedIDs(a practiceWriteArgs) []practiceHubNamed {
	out := make([]practiceHubNamed, 0, 2)
	seen := map[string]bool{}
	add := func(param, hub string) {
		if hub == "" || seen[hub] {
			return
		}
		seen[hub] = true
		out = append(out, practiceHubNamed{param: param, hub: hub})
	}
	add("source_hub", a.SourceHub)
	if a.Operation == "" {
		// The standalone delete tool has no operation field; its hub axis is
		// `source`, which the mutate arms spell `source_hub`.
		add("source", a.Source)
	}
	for role, b := range practiceHubBodies(a) {
		add(role+".metadata."+kgtypes.MetaKeySourceHub, b.Metadata[kgtypes.MetaKeySourceHub])
	}
	return out
}

// practiceHubCreatedIDs is the id set a create payload brings into existence.
func practiceHubCreatedIDs(a practiceWriteArgs) map[string]bool {
	out := map[string]bool{}
	if a.Operation != "create" && a.Operation != "create_batch" {
		return out
	}
	if a.ID != "" {
		out[a.ID] = true
	}
	for _, b := range a.Nodes {
		if b.ID != "" {
			out[b.ID] = true
		}
	}
	return out
}

// practiceNodesByIDs is this package's own bulk by-ids read, with tombstones, so
// a deleted hub is a different answer from a missing one. The engine cannot
// import the topology helper that does this elsewhere — that package imports the
// engine — so the plan is built here.
func practiceNodesByIDs(
	ctx context.Context, exec ExecuteFn, ids []string,
) (map[string]*knowledgev1.Node, error) {
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Ids: ids, IncludeTombstones: true,
		}},
		Target: &knowledgev1.GraphSelector{Graph: string(kgtypes.GraphPractice)},
	})
	if err != nil {
		return nil, err
	}
	if resp.GetTruncated() {
		// THE READ REPORTS TRUNCATION RATHER THAN ACTING ON IT, and a caller that
		// ignored the verdict would be treating a hub it never saw as absent —
		// refusing a good write — or, worse, admitting one it never checked.
		return nil, fmt.Errorf(
			"the hub read came back TRUNCATED, so this call did not see every id it asked for and cannot " +
				"tell a hub that is missing from one it failed to read")
	}
	nodes, derr := DecodeNodes(resp)
	if derr != nil {
		return nil, derr
	}
	out := make(map[string]*knowledgev1.Node, len(nodes))
	for _, n := range nodes {
		out[n.GetId()] = n
	}
	return out, nil
}
