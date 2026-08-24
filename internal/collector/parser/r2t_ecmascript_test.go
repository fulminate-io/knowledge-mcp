// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// requireTypedQualifierCall asserts a CALLS edge from caller to target that the
// TYPED-QUALIFIER rung produced.
//
// THE RULE IS ASSERTED, NOT JUST THE EDGE. A single-candidate bound edge carries
// its rung in Method, so an edge that reached the same target through a lower
// rung — a same-file name match, a dot scope — is a DIFFERENT outcome that an
// endpoint-only assertion cannot tell apart. Every subtest in this file exists
// to prove one route of the typed-qualifier arm, so an edge attributed to any
// other rung is a failure of exactly the thing under test.
func requireTypedQualifierCall(t *testing.T, res PopulateResult, caller, target string) {
	t.Helper()
	var methods []string
	for _, e := range res.Edges {
		if e.Type != string(kgtypes.EdgeCalls) || e.FromId != caller || e.ToId != target {
			continue
		}
		if e.Method == string(RuleTypedQualifier) {
			return
		}
		methods = append(methods, e.Method)
	}
	require.Failf(t, "no typed-qualifier CALLS edge",
		"expected %s -> %s with Method %q; edges between that pair carried %v",
		caller, target, string(RuleTypedQualifier), methods)
}

// requireNoCallTo asserts that no CALLS edge from caller reaches target at all.
func requireNoCallTo(t *testing.T, res PopulateResult, caller, target string) {
	t.Helper()
	for _, e := range res.Edges {
		if e.Type == string(kgtypes.EdgeCalls) && e.FromId == caller && e.ToId == target {
			require.Failf(t, "unexpected CALLS edge", "%s -> %s (Method %q)", caller, target, e.Method)
		}
	}
}

// TestR2TBindsECMAScript proves the typescript, tsx and javascript qualifier and
// type-facts arms bind END TO END — through the real chunker, the real
// declaration index and the real resolution ladder — rather than merely
// producing a map some unit test reads back.
//
// EACH SUBTEST ISOLATES ONE ROUTE, and that separation is the point rather than
// symmetry: the two Fields sources, the call-return route and the direct-type
// route are four independent pieces of arm, and a single combined fixture would
// go green with three of them broken.
func TestR2TBindsECMAScript(t *testing.T) {
	t.Run("typescript_annotated_param", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/app.ts", src: "class Config {\n  load(): void {}\n}\n\n" +
				"class Runner {\n  run(cfg: Config): void {\n    cfg.load();\n  }\n}\n"},
		}, nil)
		requireTypedQualifierCall(t, res, "web/app.ts:Runner.run", "web/app.ts:Config.load")
	})

	t.Run("tsx_this_receiver", func(t *testing.T) {
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/Panel.tsx", src: "export class Panel {\n  render(): void {}\n\n" +
				"  show(): void {\n    this.render();\n  }\n}\n"},
		}, nil)
		requireTypedQualifierCall(t, res, "web/Panel.tsx:Panel.show", "web/Panel.tsx:Panel.render")
	})

	t.Run("this_field_hop", func(t *testing.T) {
		// THE PARAMETER-PROPERTY FIELD SOURCE, END TO END. `private store: Store`
		// declares the field `this.store` and produces NO field-definition node,
		// so an arm reading only public_field_definition fails here and nowhere
		// else. There is deliberately no plain field anywhere in this fixture.
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/svc.ts", src: "class Store {\n  get(): void {}\n}\n\n" +
				"class Server {\n  constructor(private store: Store) {}\n\n" +
				"  run(): void {\n    this.store.get();\n  }\n}\n"},
		}, nil)
		requireTypedQualifierCall(t, res, "web/svc.ts:Server.run", "web/svc.ts:Store.get")
	})

	t.Run("plain_field_hop", func(t *testing.T) {
		// THE OTHER FIELD SOURCE, and a separate subtest for the reason above
		// rather than for symmetry: an arm reading ONLY parameter properties
		// passes this_field_hop and fails here. There is deliberately no
		// constructor parameter property anywhere in this fixture.
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/plain.ts", src: "class Store {\n  get(): void {}\n}\n\n" +
				"class Server {\n  store: Store;\n\n" +
				"  run(): void {\n    this.store.get();\n  }\n}\n"},
		}, nil)
		requireTypedQualifierCall(t, res, "web/plain.ts:Server.run", "web/plain.ts:Store.get")
	})

	t.Run("call_return_binds", func(t *testing.T) {
		// THE ONLY CONSUMER OF TypeFacts.Results. Route 4 records the callee with
		// FromCall set and the rung reads that callee's DECLARED RESULT TYPE back
		// out of Results — so an arm that never populates Results at all passes
		// every other assertion in this file and fails only here.
		//
		// SAME-FILE ON PURPOSE: the index-aware type-text helper is wired into the
		// direct-type arm and NOT into the call-return route, so the cross-file
		// form of this shape does not bind today and is reported as a measured
		// number rather than asserted here.
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/mk.ts", src: "class Client {\n  send(): void {}\n}\n\n" +
				"function makeClient(): Client {\n  return new Client();\n}\n\n" +
				"class Caller {\n  go(): void {\n    const c = makeClient();\n    c.send();\n  }\n}\n"},
		}, nil)
		requireTypedQualifierCall(t, res, "web/mk.ts:Caller.go", "web/mk.ts:Client.send")
	})

	t.Run("javascript_new_local", func(t *testing.T) {
		// javascript has no annotation syntax at all, so the constructor local is
		// the whole of its direct-type route.
		res := populateRepoFixture(t, []fixtureFile{
			{path: "tools/run.mjs", src: "class Client {\n  send() {}\n}\n\n" +
				"export function go() {\n  const c = new Client();\n  c.send();\n}\n"},
		}, nil)
		requireTypedQualifierCall(t, res, "tools/run.mjs:go", "tools/run.mjs:Client.send")
	})

	t.Run("imported_type_binds", func(t *testing.T) {
		// THE CROSS-FILE DIRECT-TYPE ROUTE. A parameter annotated with a type
		// IMPORTED from another file resolves through the index-aware type-text
		// helper, which consults the declaring file's binds. Before that helper
		// was wired in, this bound nothing — so this subtest is the F3-side
		// catcher for the same widening F0 proved on a stand-in arm, now driven by
		// the real TypeScript walk.
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/config.ts", src: "export class Config {\n  load(): void {}\n}\n"},
			{path: "web/run.ts", src: "import { Config } from './config';\n\n" +
				"export class Runner {\n  run(cfg: Config): void {\n    cfg.load();\n  }\n}\n"},
		}, nil)
		requireTypedQualifierCall(t, res, "web/run.ts:Runner.run", "web/config.ts:Config.load")

		// KNOWN-NEGATIVE CONTROL: the importing file declares no Config of its
		// own, so the edge above cannot be a same-file name hit wearing the
		// cross-file label.
		requireNoCallTo(t, res, "web/run.ts:Runner.run", "web/run.ts:Config.load")
	})
}
