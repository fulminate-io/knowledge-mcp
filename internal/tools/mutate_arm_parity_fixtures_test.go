// SPDX-License-Identifier: Apache-2.0

package tools

// mutate_arm_parity_fixtures_test.go holds the per-arm drive fixtures the parity
// harness runs. Split from mutate_arm_parity_test.go (which holds the harness
// and its probe rules) only to keep both inside the repo's file-length gate.
//
// One fixture per dispatch arm. `base` is the MINIMAL payload that selects that
// arm and lets it reach its write; `discriminants` names the params whose value
// selects the arm, mapped to an ARM-PRESERVING probe (see the harness doc for
// why an arbitrary probe on those measures nothing). Everything the fixture
// seeds is a real node the fake answers for, because an unseeded id turns a
// consumed-param row into a silently-dropped edge.

import (
	"context"
	"encoding/json"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// Seeded fixture ids. Every id a probe can reference resolves in the fake, so a
// consumed-param assertion measures routing rather than a dropped lookup.
const (
	paritySeedStep      = "parity-step"
	paritySeedResearch  = "parity-research"
	paritySeedCriterion = "parity-criterion"
	paritySeedBackend   = "parity-backend"
	paritySeedPlan      = "parity-plan"
	paritySeedChild     = "parity-plan-child"
	paritySeedLocalA    = "parity-local-a"
	paritySeedLocalB    = "parity-local-b"
	paritySeedTicket    = "parity-ticket"
	paritySeedRule      = "parity-rule"
)

// parityFixture describes how to drive one arm through the fake.
type parityFixture struct {
	// base is the minimal arm-selecting payload.
	base map[string]any
	// discriminants maps each arm-selecting param to an arm-PRESERVING probe.
	// A param listed here is observed as "the arm is still selected", never as a
	// literal in the write — injecting an arbitrary value would deselect the arm
	// and the row would measure a different arm's behavior.
	discriminants map[string]any
	// paramBase replaces `base` for one param. Needed by the two arms whose
	// consumed set cannot be exercised from a single payload shape: the
	// operation-polymorphic passthrough (its set is a union over create /
	// create_batch / update / delete) and the per-type update router (whose
	// per-node-type refinement rejects a param on a node type that does not own
	// it). Driving those rows in the shape that actually reads the param is what
	// makes the assertion meaningful rather than vacuous.
	paramBase map[string]map[string]any
	// declines is true when the arm returns handled==false and the engine owns
	// the write; those rows assert the consumed class against engine.Compile.
	declines bool
	// noAccounting marks an arm that runs no gate at all (empty rejected AND
	// empty deliberatelyIgnored). Set from the armSpec shape by the harness, not
	// by hand — see parityFixtures.
	noAccounting bool
	// viaCriterionIntercept routes through InterceptAddCriterion, which fires
	// ahead of InterceptMutate for criterion creates.
	viaCriterionIntercept bool
}

// paritySeed builds the fake with every fixture node seeded. One shared seed
// keeps a probe referencing any fixture id resolvable from any arm's row.
func paritySeed(t *testing.T) *fakeGraphCaller {
	t.Helper()
	child := &knowledgev1.Node{Id: paritySeedChild, Type: "step", Status: "active"}
	return &fakeGraphCaller{
		queryResponses: map[string]kgtools.ToolResult{
			paritySeedStep:      nodeResultJSON(t, paritySeedStep, "step", map[string]string{}),
			paritySeedResearch:  parityNodeWithName(t, paritySeedResearch, "research", "parity question?"),
			paritySeedCriterion: nodeResultJSON(t, paritySeedCriterion, "criterion", map[string]string{}),
			paritySeedPlan:      nodeResultJSON(t, paritySeedPlan, "plan", map[string]string{}),
			paritySeedChild:     nodeResultJSON(t, paritySeedChild, "step", map[string]string{}),
			paritySeedLocalA:    nodeResultJSON(t, paritySeedLocalA, "finding", map[string]string{}),
			paritySeedLocalB:    nodeResultJSON(t, paritySeedLocalB, "finding", map[string]string{}),
			paritySeedTicket:    nodeResultJSON(t, paritySeedTicket, "ticket", map[string]string{}),
			paritySeedRule:      nodeResultJSON(t, paritySeedRule, "rule", map[string]string{}),
			paritySeedBackend: nodeResultJSON(t, paritySeedBackend, "ticket", map[string]string{
				"backend": "linear", "linear_id": "uuid-parity",
			}),
		},
		traversalByRoot: map[string][]*knowledgev1.Node{
			paritySeedPlan: {child},
		},
		mutateIDs:    []string{"parity-created-1"},
		mutateResult: kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: `{"ids":["parity-created-1"]}`}}},
	}
}

// parityNodeWithName seeds a node carrying symbol_name, which the answer arm
// reads to build its derived summary.
func parityNodeWithName(t *testing.T, id, typ, symbolName string) kgtools.ToolResult {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"id": id, "type": typ, "symbol_name": symbolName, "metadata": map[string]string{},
	})
	if err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	return kgtools.ToolResult{Content: []kgtools.ContentBlock{{Type: "text", Text: string(b)}}}
}

// armParityDeps wraps the fake with a configured backend so the tracker
// write-through arm can complete.
func armParityDeps(fc *fakeGraphCaller) interceptTestDeps {
	return interceptTestDeps{byName: map[string]backends.Backend{"linear": &fakeBackend{}}, gc: fc}
}

// parityDrive runs one payload through the arm's entry point.
func parityDrive(fx parityFixture, fc *fakeGraphCaller, payload []byte) (bool, kgtools.ToolResult) {
	params := kgtools.CallToolParams{Name: "mutate", Arguments: json.RawMessage(payload)}
	if fx.viaCriterionIntercept {
		return InterceptAddCriterion(context.Background(), armParityDeps(fc), params)
	}
	return InterceptMutate(opCtx(), armParityDeps(fc), params)
}

// parityFixtures returns the drive fixture for every registered arm. The
// noAccounting flag is derived from the armSpec SHAPE (an arm with an empty
// rejected set and an empty deliberatelyIgnored set runs no gate), never from a
// hardcoded arm name, so a future no-accounting arm inherits the exemption.
func parityFixtures() map[armID]parityFixture {
	fx := map[armID]parityFixture{
		armCriterionCreate: {
			viaCriterionIntercept: true,
			base: map[string]any{
				"operation": "create", "type": "criterion",
				"step_id": paritySeedStep, "description": "probe-description",
				"summary": "probe-summary for the criterion",
			},
			discriminants: map[string]any{"operation": "create", "type": "criterion"},
		},
		armCreateFinding: {
			base: map[string]any{
				"operation": "create", "type": "finding",
				"name": "probe-name", "summary": "probe-summary for the finding",
			},
			discriminants: map[string]any{"operation": "create", "type": "finding", "graph": "knowledge"},
		},
		armCreateResearch: {
			base: map[string]any{
				"operation": "create", "type": "research",
				"name": "probe-name", "summary": "probe-summary for the research",
			},
			discriminants: map[string]any{"operation": "create", "type": "research", "graph": "knowledge"},
		},
		armCreateRule: {
			base: map[string]any{
				"operation": "create", "type": "rule",
				"name": "probe-name", "summary": "probe-summary for the rule",
			},
			discriminants: map[string]any{"operation": "create", "type": "rule", "graph": "knowledge"},
		},
		// The context-linked create arm is selected by the PRESENCE of a context
		// param, so ticket_id is in the base rather than in the discriminants —
		// without it the payload lands on the fallthrough arm instead.
		//
		// SPENDING ticket_id HERE IS DELIBERATE: a discriminant row is
		// selection-only whatever its declared class, so exactly one of the trio
		// loses its literal parity assertion. ticket_id is the one to spend
		// because its routing is literally asserted elsewhere — every leg of the
		// type-blind test checks a ticket--contains-->newNode edge — which leaves
		// both links and session with real literal assertions here.
		armCreateContextLinked: {
			base: map[string]any{
				"operation": "create", "type": "document",
				"name": "probe-name", "summary": "probe-summary for the context-linked create",
				"ticket_id": paritySeedTicket,
			},
			discriminants: map[string]any{
				"operation": "create", "type": "document", "graph": "knowledge",
				"ticket_id": paritySeedTicket,
			},
		},
		// THE FALLTHROUGH PROBE CARRIES AN EMPTY TRIO, and that is what keeps it on
		// this arm: any non-empty context param deselects the fallthrough in favor
		// of armCreateContextLinked. suppliedMutateParams treats an empty value as
		// absent, so the three empty entries survive the arm's own rejected rows
		// without supplying anything.
		//
		// "memory" is a generic create type with no client handler, so it reaches
		// the engine CREATE arm — which is exactly what this row measures. Every
		// generic type other than the three claimed ones is an equally valid probe.
		armCreateFallthrough: {
			declines: true,
			base: map[string]any{
				"operation": "create", "type": "memory", "name": "probe-name",
				"ticket_id": "", "session": "", "links": []any{},
			},
			discriminants: map[string]any{
				"operation": "create", "type": "memory", "graph": "knowledge",
				"ticket_id": "", "session": "", "links": []any{},
			},
		},
		armCreateBatch: {
			declines: true,
			base: map[string]any{
				"operation": "create_batch",
				"nodes":     []any{map[string]any{"type": "finding", "name": "probe-nodes", "summary": "probe-nodes summary"}},
			},
			discriminants: map[string]any{"operation": "create_batch", "graph": "knowledge"},
		},
		armUpsert: {
			declines: true,
			base: map[string]any{
				"operation": "upsert", "type": "document",
				"id": paritySeedLocalA, "name": "probe-name",
			},
			discriminants: map[string]any{"operation": "upsert", "type": "document", "graph": "knowledge", "id": paritySeedLocalA},
		},
		armUpdateBackend: {
			base: map[string]any{
				"operation": "update", "id": paritySeedBackend, "name": "probe-name",
			},
			discriminants: map[string]any{"operation": "update", "graph": "knowledge", "id": paritySeedBackend},
		},
		armUpdateRollup: {
			base: map[string]any{
				"operation": "update", "id": paritySeedPlan, "status": "completed",
			},
			discriminants: map[string]any{
				"operation": "update", "graph": "knowledge", "id": paritySeedPlan,
				"status": "completed", "expand_to_descendants": true,
			},
		},
		armUpdateTyped: {
			base: map[string]any{
				"operation": "update", "id": paritySeedCriterion, "command": "probe-command",
			},
			discriminants: map[string]any{"operation": "update", "graph": "knowledge", "id": paritySeedCriterion},
			// The per-node-type refinement rejects a per-type param on a node type
			// that does not own it, so each is driven against its OWNING type:
			// scope/enforcement belong to a rule, evidence to a finding.
			//
			// `name` is here for the mirror-image reason: it is consumed by this
			// arm for every type EXCEPT criterion, where the name is derived from
			// the description and a supplied one is rejected. The default base
			// drives a criterion, so name is driven against a rule — the type on
			// which its declared CONSUMED class is observable.
			paramBase: map[string]map[string]any{
				"scope":       {"operation": "update", "id": paritySeedRule},
				"enforcement": {"operation": "update", "id": paritySeedRule},
				"evidence":    {"operation": "update", "id": paritySeedLocalA},
				"name":        {"operation": "update", "id": paritySeedRule},
			},
		},
		armUpdateFallthrough: {
			declines: true,
			base: map[string]any{
				"operation": "update", "id": paritySeedTicket, "name": "probe-name",
			},
			discriminants: map[string]any{"operation": "update", "graph": "knowledge", "id": paritySeedTicket},
		},
		armUpdateBatchIDs: {
			declines: true,
			base: map[string]any{
				"operation": "update", "ids": []any{paritySeedLocalA, paritySeedLocalB},
				"status": "probe-status",
			},
			discriminants: map[string]any{
				"operation": "update", "graph": "knowledge",
				"ids": []any{paritySeedLocalA, paritySeedLocalB},
				// A present `id` normalizes the call to the SINGLE-id path, so it
				// deselects this arm: probe it absent and assert the arm survives.
				"id": "",
			},
		},
		armUpdateBatchItems: {
			declines: true,
			base: map[string]any{
				"operation": "update_batch",
				"items":     []any{map[string]any{"id": paritySeedLocalA, "summary": "probe-items"}},
			},
			discriminants: map[string]any{"operation": "update_batch", "graph": "knowledge"},
		},
		armBulkUpdateMetadata: {
			declines: true,
			base: map[string]any{
				"operation": "bulk_update_metadata",
				"updates": []any{map[string]any{
					"id": paritySeedLocalA, "metadata": map[string]any{"probe-updates-key": "probe-updates"},
				}},
			},
			discriminants: map[string]any{"operation": "bulk_update_metadata", "graph": "knowledge"},
		},
		armDelete: {
			base: map[string]any{
				"operation": "delete", "ids": []any{paritySeedLocalA},
			},
			discriminants: map[string]any{
				"operation": "delete", "graph": "knowledge",
				"ids": []any{paritySeedLocalA}, "id": paritySeedLocalA,
			},
		},
		armAnswer: {
			base: map[string]any{
				"operation": "answer", "id": paritySeedResearch, "conclusion": "probe-conclusion",
				"summary": "the probe question is concluded",
			},
			discriminants: map[string]any{
				"operation": "answer", "graph": "knowledge", "id": paritySeedResearch,
				// question_id is the documented ALIAS for id — it selects the same
				// node and is only read when id is absent, so it is a discriminant.
				"question_id": paritySeedResearch,
			},
		},
		armLinkCrossGraph: {
			base: map[string]any{
				"operation": "link", "link_graph": "linkage",
				"from": paritySeedLocalA, "to": paritySeedLocalB, "relationship": "relates-to",
			},
			discriminants: map[string]any{"operation": "link", "link_graph": "linkage", "graph": "knowledge"},
		},
		armLinkFallthrough: {
			declines: true,
			base: map[string]any{
				"operation": "link",
				"from":      paritySeedLocalA, "to": paritySeedLocalB, "relationship": "relates-to",
			},
			discriminants: map[string]any{"operation": "link", "graph": "knowledge"},
			// `name` is the graph INSTANCE key here, and only a name-addressed
			// family reads one — so the row has to be driven on such a graph or it
			// measures nothing. `link` is the one operation dispatched ahead of the
			// non-knowledge guard (the cross-graph composer must see foreign
			// endpoints), which is what lets this arm run on graph:"web" at all;
			// every other arm's foreign-graph call is claimed by
			// armNonKnowledgeFallthrough instead. On the knowledge family the same
			// param would be a NODE name that reaches nothing — the mis-mapping
			// compile_mutate_selector_test.go pins closed.
			paramBase: map[string]map[string]any{
				"name": {
					"operation": "link", "graph": "web",
					"from": paritySeedLocalA, "to": paritySeedLocalB, "relationship": "relates-to",
				},
			},
		},
		armUnlink: {
			declines: true,
			base: map[string]any{
				"operation": "unlink",
				"from":      paritySeedLocalA, "to": paritySeedLocalB, "relationship": "relates-to",
			},
			discriminants: map[string]any{"operation": "unlink", "graph": "knowledge"},
		},
		armGraphPassthrough: {
			base: map[string]any{
				"operation": "create", "graph": "practice", "language": "go",
				"type": "pattern", "name": "probe-name",
			},
			discriminants: map[string]any{
				"operation": "create", "graph": "practice", "language": "go", "type": "pattern",
				// A non-empty link_graph makes the passthrough decline BEFORE its
				// gate runs, so it deselects the arm: arm-preserving is absent.
				"link_graph": "",
			},
			// This arm is OPERATION-POLYMORPHIC — its consumed set is the union of
			// what the four engine arms read, so an operation-specific param is
			// driven in the operation that reads it: nodes/edges belong to the
			// batch create, and keywords only lands as an UPDATE set_field (the
			// create NodeBody has no keywords carrier).
			paramBase: map[string]map[string]any{
				"nodes": {"operation": "create_batch", "graph": "practice", "language": "go"},
				"edges": {
					"operation": "create_batch", "graph": "practice", "language": "go",
					"nodes": []any{map[string]any{"type": "pattern", "name": "probe-anchor", "summary": "probe anchor"}},
				},
				"keywords": {
					"operation": "update", "graph": "practice", "language": "go",
					"id": paritySeedLocalA,
				},
			},
		},
		// The repo entry is REQUIRED, not decoration: a code-graph mutate with no
		// repo is now refused by requireGraphInstanceSelector before it can
		// decline to the engine, so a repo-less payload would be claimed by that
		// refusal and measure nothing here. The old payload exercised a shape
		// that could never have succeeded against any real server.
		armNonKnowledgeFallthrough: {
			declines: true,
			base: map[string]any{
				"operation": "update", "graph": "code", "repo": "probe-repo",
				"id": paritySeedLocalA, "name": "probe-name",
			},
			discriminants: map[string]any{
				"operation": "update", "graph": "code", "repo": "probe-repo", "id": paritySeedLocalA,
			},
		},
	}
	for arm, spec := range mutateArmRegistry {
		f := fx[arm]
		f.noAccounting = len(spec.rejected) == 0 && len(spec.deliberatelyIgnored) == 0
		fx[arm] = f
	}
	return fx
}
