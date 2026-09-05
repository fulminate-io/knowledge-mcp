// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// compileMutateForTest compiles a mutate args JSON and returns the request,
// requiring the shape to be reducible. Every case below asserts on the TARGET,
// so the helper deliberately returns the whole request rather than the plan.
func compileMutateForTest(t *testing.T, args string) *knowledgev1.ExecuteRequest {
	t.Helper()
	req, ok := Compile("mutate", json.RawMessage(args))
	require.True(t, ok, "mutate args must compile to a MutationPlan: %s", args)
	return req
}

// targetNameOf reads the Target's instance Name, tolerating a nil Target (the
// no-selector-fields case buildTarget returns for a bare knowledge write).
func targetNameOf(req *knowledgev1.ExecuteRequest) string {
	return req.GetTarget().GetName()
}

// TestCompileMutate_NodeNameNeverRidesTheGraphSelector is the regression pin for
// the P1 the per-family selector reject exposed: `name` on the mutate tool is the
// NODE name ("Node name or title"), and every mutate arm used to copy it verbatim
// into GraphSelector.Name. The server discarded sel.Name for the knowledge family
// until validateGraphSelector started rejecting it, at which point every write
// carrying a node name — a typed create, a criterion description update (whose
// name is derived from the description), a log-backend upsert — failed with
// "graph=knowledge holds ONE graph: name= is a label, not a selector".
//
// The mutate surface has NO graph-instance selector: graph picks the family,
// repo/account/language pick the instance for the families that have one, and the
// only name-addressed instance any mutate arm targets is the transformers bucket,
// which is pinned to a literal (see the sibling test below). So the assertion here
// is total across every operation: a node name never reaches the selector.
func TestCompileMutate_NodeNameNeverRidesTheGraphSelector(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{
			// The live repro row: mutate(create, type:"observation", name:...) reaches
			// engine.Compile with the node name at top level (the intercept declines it,
			// so InterceptMutate hands the raw args to engine.Dispatch).
			name: "create carries the node name at top level",
			args: `{"operation":"create","type":"observation","name":"g48-update-arm-repro-scratch",` +
				`"summary":"scratch probe","description":"scratch probe"}`,
		},
		{
			// The third measured repro row, and the one that proves the failing set
			// partitions on NAME-PRESENCE rather than on node type: document and
			// observation are both plain generic creates, and neither reaches any
			// type-specific compile path.
			name: "create carries the node name on a second plain type",
			args: `{"operation":"create","type":"document","name":"g48 doc","summary":"s","description":"d"}`,
		},
		{
			name: "create_batch with a top-level name alongside nodes[]",
			args: `{"operation":"create_batch","name":"a node name","nodes":[{"type":"finding","name":"n","summary":"s"}]}`,
		},
		{
			// upsertLogBackend (tools_logs_manage_backend.go) sends the backend's
			// display name here on every configure_log_backend call.
			name: "upsert carries the node name",
			args: `{"operation":"upsert","type":"log-backend","id":"lb1","name":"prod-loki","description":"d"}`,
		},
		{
			// The live repro row: a criterion description update derives name=description
			// in the typed router and forwards it (intercept_mutate_update.go).
			name: "by-id update carries the node name",
			args: `{"operation":"update","id":"n1","name":"the suite is green","description":"the suite is green"}`,
		},
		{
			name: "by-ids batch update carries the node name",
			args: `{"operation":"update","ids":["n1","n2"],"name":"shared name","status":"completed"}`,
		},
		{
			name: "update_batch carries a top-level node name",
			args: `{"operation":"update_batch","name":"a node name","items":[{"id":"n1","summary":"s"}]}`,
		},
		{
			name: "bulk_update_metadata carries a top-level node name",
			args: `{"operation":"bulk_update_metadata","name":"a node name",` +
				`"updates":[{"id":"n1","metadata":{"k":"v"}}]}`,
		},
		{
			name: "link carries a top-level node name",
			args: `{"operation":"link","from":"a","to":"b","relationship":"relates-to","name":"a node name"}`,
		},
		{
			name: "unlink carries a top-level node name",
			args: `{"operation":"unlink","from":"a","to":"b","relationship":"relates-to","name":"a node name"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := compileMutateForTest(t, tc.args)
			assert.Empty(t, targetNameOf(req),
				"the mutate `name` param is the NODE name — it must never ride GraphSelector.Name")
		})
	}
}

// TestCompileMutate_NamelessShapesNeverCarriedASelector is the NEGATIVE half of
// the matrix, and it is what makes the diagnosis falsifiable rather than merely
// consistent. These shapes emitted a nil Target BEFORE the fix too, so they never
// failed — which locates the defect at the caller-supplied `name` and rules out
// the competing explanations the repro matrix invites: that some node TYPE, or
// the presence of `description`, routes through a generic build that synthesizes
// a label into the selector. Nothing is synthesized anywhere in this package.
//
// The one place a name IS synthesized is the typed-update router, which derives
// name=description for a CRITERION alone (intercept_mutate_update.go) — which is
// why a description-only update fails on a criterion and succeeds on every other
// type, and why the same criterion's metadata-only update succeeds.
func TestCompileMutate_NamelessShapesNeverCarriedASelector(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"create document with no name", `{"operation":"create","type":"document","summary":"s","description":"d"}`},
		{"create observation with no name", `{"operation":"create","type":"observation","summary":"s","description":"d"}`},
		{"update description only", `{"operation":"update","id":"n1","description":"d"}`},
		{"update metadata only", `{"operation":"update","id":"n1","metadata":{"k":"v"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := compileMutateForTest(t, tc.args)
			assert.Nil(t, req.GetTarget(),
				"a nameless mutate names no graph instance — this shape never carried a selector")
		})
	}
}

// TestCompileMutate_KnowledgeFamilyTargetCarriesNoInstanceFields pins the whole
// knowledge-family selector, not just Name: a knowledge-graph write addresses the
// one singleton graph, so the compiled Target must carry no instance field at all
// (buildTarget collapses the all-empty case to a nil selector). Repo/Account/
// Language/Branch are the same silent-discard class the per-family reject closed.
//
// ITS KNOWN-POSITIVE IS TestCompileMutate_NameAddressedFamiliesStillRoute below,
// which asserts Target.Name == "the-instance" for logs / web / pdf / a registered
// custom type. Without one, a compiler that could no longer put ANYTHING in
// Target.Name would satisfy the emptiness here vacuously. (It used to be a
// transformers bucket-pin test; that family was removed, and the name-addressed
// control covers the same property.)
func TestCompileMutate_KnowledgeFamilyTargetCarriesNoInstanceFields(t *testing.T) {
	req := compileMutateForTest(t,
		`{"operation":"update","id":"n1","name":"a node name","description":"d"}`)
	assert.Nil(t, req.GetTarget(),
		"a bare knowledge write names no graph instance, so it should emit no selector at all")
}

// TestCompileMutate_NameAddressedFamiliesStillRoute is the control that keeps the
// fix from being a blanket drop. logs / web / pdf and registered custom types
// address their instance BY NAME, so `name` on those is the graph-instance key —
// which is what graphsel.ApplyInstanceKey assigns for the pipeline write-back
// (pipeline/rpc.go writeBatchUpdates), the one production caller that routes a
// mutate on a foreign graph. Dropping it there would silently send every
// summary/vector write-back for those graphs to the wrong place.
func TestCompileMutate_NameAddressedFamiliesStillRoute(t *testing.T) {
	for _, graph := range []string{"logs", "web", "pdf", "a-registered-custom-type"} {
		t.Run(graph, func(t *testing.T) {
			req := compileMutateForTest(t,
				`{"operation":"update_batch","graph":"`+graph+`","name":"the-instance",`+
					`"items":[{"id":"n1","summary":"s"}]}`)
			require.NotNil(t, req.GetTarget())
			assert.Equal(t, graph, req.GetTarget().GetGraph())
			assert.Equal(t, "the-instance", targetNameOf(req),
				"a name-addressed family routes its instance by name — the write-back depends on it")
		})
	}
}

// TestCompileMutate_TypedInstanceFieldsStillRide is the second known-positive
// control: dropping the node name from the selector must not disturb the
// per-family instance discriminants the batch write-back paths depend on
// (code by repo@branch, cloud/cicd by account, practice by language).
func TestCompileMutate_TypedInstanceFieldsStillRide(t *testing.T) {
	t.Run("code routes by repo and branch", func(t *testing.T) {
		req := compileMutateForTest(t,
			`{"operation":"update_batch","graph":"code","repo":"myrepo","branch":"feat",`+
				`"name":"a node name","items":[{"id":"a","summary":"s"}]}`)
		require.NotNil(t, req.GetTarget())
		assert.Equal(t, "myrepo", req.GetTarget().GetRepo())
		assert.Equal(t, "feat", req.GetTarget().GetBranch())
		assert.Empty(t, targetNameOf(req))
	})
	t.Run("cloud routes by account", func(t *testing.T) {
		req := compileMutateForTest(t,
			`{"operation":"create","graph":"cloud","account":"aws-123","type":"resource",`+
				`"name":"a node name","summary":"s"}`)
		require.NotNil(t, req.GetTarget())
		assert.Equal(t, "aws-123", req.GetTarget().GetAccount())
		assert.Empty(t, targetNameOf(req))
	})
	t.Run("practice routes by language", func(t *testing.T) {
		req := compileMutateForTest(t,
			`{"operation":"create","graph":"practice","language":"go","type":"pattern",`+
				`"name":"a node name","summary":"s"}`)
		require.NotNil(t, req.GetTarget())
		assert.Equal(t, "go", req.GetTarget().GetLanguage())
		assert.Empty(t, targetNameOf(req))
	})
}

// TestCompileMutate_ChecksSelectorCarriesNoInstanceField is the CLIENT half of
// the round-trip pin for the checks singleton, and it is a regression test for a
// defect that reached production: every WRITE to the checks graph was refused
// while reads worked.
//
// WHY READS WORKED AND WRITES DID NOT. The read paths build their selector with
// an explicitly empty instance name, so they were correct by construction and
// the scanner's own tests passed. Writes go through mutateTargetName, and checks
// was missing from nameBlindGraphFamilies — so the mutate tool's `name` param,
// which is the NODE name ("Node name or title"), rode into GraphSelector.Name.
// The server then refused the write: "graph=checks holds ONE graph: it does not
// accept name=".
//
// This is the same defect the sibling test above pins for the knowledge family.
// It recurred because checks is a THIRD copy of one partition — the server's
// selectorFieldPolicies, this file's nameBlindGraphFamilies, and
// graphsel.InstanceField all encode "which field addresses an instance of this
// family", and adding a family to one does not add it to the others.
//
// THE SERVER HALF is TestValidateGraphSelector_ChecksWriteShapeAccepted, which
// feeds this exact shape to validateGraphSelector. The two cannot be one test:
// no shared hand-written package spans the client and server modules, so the
// round trip is pinned as a matched pair that must be changed together.
func TestCompileMutate_ChecksSelectorCarriesNoInstanceField(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{
			// THE LIVE REPRO. A check create carries the node name at top level,
			// exactly as the calibration lane's first seeding attempt did.
			name: "create carries the node name",
			args: `{"operation":"create","graph":"checks","type":"finding","name":"no naked defer Close",` +
				`"summary":"close errors must be handled","metadata":{"check_type":"ast_pattern"}}`,
		},
		{
			name: "create_batch carries a top-level node name",
			args: `{"operation":"create_batch","graph":"checks","name":"a node name",` +
				`"nodes":[{"type":"finding","name":"n","summary":"s"}]}`,
		},
		{
			// The shape a language-qualified id is authored through.
			name: "upsert carries an id and a node name",
			args: `{"operation":"upsert","graph":"checks","type":"finding","id":"go:no-naked-defer",` +
				`"name":"no naked defer Close","summary":"s"}`,
		},
		{
			name: "by-id update carries the node name",
			args: `{"operation":"update","graph":"checks","id":"go:chk-1","name":"renamed","description":"d"}`,
		},
		{
			name: "fixture example create carries the node name",
			args: `{"operation":"create","graph":"checks","type":"example","name":"fx-bad","summary":"s",` +
				`"content":"package p","metadata":{"language":"go"}}`,
		},
		// THE ROWS THAT SUPPLY A TOP-LEVEL language. Everything above carries
		// language only INSIDE a metadata payload, which never reaches the
		// selector — so the language assertion below, though correctly worded,
		// could not fail against any of them. A field assertion is only as strong
		// as the input space its fixtures span: to assert a field stays empty,
		// some case must supply the input that would populate it.
		//
		// A caller passes a top-level language here for the obvious reason: it is
		// how practice writes are addressed, and checks is the sibling family.
		{
			name: "create carries a top-level language",
			args: `{"operation":"create","graph":"checks","language":"go","type":"finding",` +
				`"name":"no naked defer Close","summary":"s"}`,
		},
		{
			name: "upsert carries a top-level language",
			args: `{"operation":"upsert","graph":"checks","language":"go","type":"finding",` +
				`"id":"go:no-naked-defer","name":"n","summary":"s"}`,
		},
		{
			name: "by-id update carries a top-level language",
			args: `{"operation":"update","graph":"checks","language":"go","id":"go:chk-1","status":"active"}`,
		},
		{
			name: "update_batch carries a top-level language",
			args: `{"operation":"update_batch","graph":"checks","language":"go",` +
				`"items":[{"id":"go:chk-1","summary":"s"}]}`,
		},
		{
			name: "bulk_update_metadata carries a top-level language",
			args: `{"operation":"bulk_update_metadata","graph":"checks","language":"go",` +
				`"updates":[{"id":"go:chk-1","metadata":{"severity":"warning"}}]}`,
		},
		{
			name: "delete carries a top-level language",
			args: `{"operation":"delete","graph":"checks","language":"go","ids":["go:chk-1"]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := compileMutateForTest(t, tc.args)
			target := req.GetTarget()
			assert.Equal(t, "checks", target.GetGraph(),
				"the family must still be routed to checks")
			assert.Emptyf(t, targetNameOf(req),
				"the node name rode into GraphSelector.Name; checks is a singleton and the server refuses a name (args: %s)", tc.args)
			assert.Emptyf(t, target.GetLanguage(),
				"checks addresses no per-language instance; a language on the selector is a field its resolver cannot honor (args: %s)", tc.args)
		})
	}

	// KNOWN-POSITIVE CONTROL, same run: a family that DOES address an instance by
	// name must still carry it. Without this, blanking every name everywhere —
	// or a compile that silently produced a nil Target — would satisfy every
	// assertion above while breaking the families that need the field.
	//
	// It must run through mutationRequest, the SAME Target builder the cases
	// above use. deleteRequest is not usable here: it passes an empty name
	// unconditionally, so a delete-based control asserts nothing about the
	// name-blind partition and fails for its own unrelated reason.
	logs := compileMutateForTest(t,
		`{"operation":"update","graph":"logs","name":"query-123","id":"n1","status":"archived"}`)
	assert.Equal(t, "query-123", targetNameOf(logs),
		"control: logs is name-addressed and must keep its instance name, or the assertions above prove nothing")
}
