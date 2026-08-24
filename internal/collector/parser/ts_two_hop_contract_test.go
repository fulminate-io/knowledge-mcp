// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// callTargetsFrom returns every CALLS target of one declaration, with the Method
// each edge carries.
func callTargetsFrom(res PopulateResult, from string) map[string]string {
	out := map[string]string{}
	for _, e := range res.Edges {
		if e.Type == string(kgtypes.EdgeCalls) && e.FromId == from {
			out[e.ToId] = e.Method
		}
	}
	return out
}

// implementsFrom returns every IMPLEMENTS target of one node, sorted, with the
// Method each edge carries.
func implementsFrom(res PopulateResult, from string) ([]string, map[string]string) {
	methods := map[string]string{}
	var targets []string
	for _, e := range res.Edges {
		if e.Type == string(kgtypes.EdgeImplements) && e.FromId == from {
			targets = append(targets, e.ToId)
			methods[e.ToId] = e.Method
		}
	}
	sort.Strings(targets)
	return targets, methods
}

// TestTSInterfaceQualifierBindsToMember proves the TWO-HOP CONTRACT MODEL for
// TypeScript: a call through an interface-typed value targets the INTERFACE's
// member declaration, and the implementers are one IMPLEMENTS hop away from it.
//
// THE FAN-OUT ABSENCE IS HALF THE PROOF. A resolver that gave up on the
// interface and fanned the call across both implementations would reach the same
// two classes a reader eventually wants, by a route that states a fact the graph
// does not mean — so the call assertion is an EXACT target set, not a
// containment check.
func TestTSInterfaceQualifierBindsToMember(t *testing.T) {
	// The declared-conformance Method every IMPLEMENTS edge below must carry,
	// read off the shared constant rather than spelled as a literal: the constant
	// is the contract, and a hardcoded copy would keep passing after a rename.
	wantMethod := kgtypes.EdgeMethodDeclaredConformance + string(treesitter.ConformImplements)

	t.Run("same_file_two_hop", func(t *testing.T) {
		// THE DIAGNOSTIC HALF. If the cross-file case below goes red, this one
		// says whether the failure is resolution (this passes, that does not) or
		// emission (both fail).
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/one.ts", src: "export interface Sink {\n  write(c: Config): void;\n}\n\n" +
				"export class FileSink implements Sink {\n  write(c: Config): void {}\n}\n\n" +
				"export class NetSink implements Sink {\n  write(c: Config): void {}\n}\n\n" +
				"export function send(s: Sink): void {\n  s.write(null);\n}\n"},
		}, nil)

		assert.Equal(t,
			map[string]string{"web/one.ts:Sink.write": string(RuleTypedQualifier)},
			callTargetsFrom(res, "web/one.ts:send"),
			"the call targets the CONTRACT's member and nothing else — no fan-out across implementations")

		targets, methods := implementsFrom(res, "web/one.ts:Sink.write")
		assert.Equal(t, []string{"web/one.ts:FileSink.write", "web/one.ts:NetSink.write"}, targets,
			"both implementers are one hop from the contract member")
		for _, target := range targets {
			assert.Equal(t, wantMethod, methods[target],
				"the member edge carries the declared clause kind, not a fabricated method-set count")
		}
	})

	t.Run("cross_file_two_hop", func(t *testing.T) {
		// THE REAL SHAPE. A TypeScript class overwhelmingly implements an
		// interface IMPORTED from another file, so a single-file fixture would
		// prove the model on the arrangement real code least often has.
		res := populateRepoFixture(t, []fixtureFile{
			{path: "web/contract.ts", src: "export interface Sink {\n  write(c: Config): void;\n}\n"},
			{path: "web/impl.ts", src: "import { Sink } from './contract';\n\n" +
				"export class FileSink implements Sink {\n  write(c: Config): void {}\n}\n\n" +
				"export class NetSink implements Sink {\n  write(c: Config): void {}\n}\n\n" +
				"export function send(s: Sink): void {\n  s.write(null);\n}\n"},
		}, nil)

		assert.Equal(t,
			map[string]string{"web/contract.ts:Sink.write": string(RuleTypedQualifier)},
			callTargetsFrom(res, "web/impl.ts:send"),
			"the call reaches the imported contract's member, and only it")

		targets, methods := implementsFrom(res, "web/contract.ts:Sink.write")
		require.Equal(t, []string{"web/impl.ts:FileSink.write", "web/impl.ts:NetSink.write"}, targets,
			"an imported supertype spelling resolves through the declaring file's binds, so both "+
				"implementers hang off the contract member across the file boundary")
		for _, target := range targets {
			assert.Equal(t, wantMethod, methods[target])
		}

		// THE TYPE-LEVEL EDGE UNDER THE MEMBER EDGES. Its absence with the member
		// edges present would mean the pairing ran without the relationship that
		// licenses it.
		typeTargets, typeMethods := implementsFrom(res, "web/contract.ts:Sink")
		assert.Equal(t, []string{"web/impl.ts:FileSink", "web/impl.ts:NetSink"}, typeTargets,
			"the type-level edge runs supertype outward to subtype")
		for _, target := range typeTargets {
			assert.Equal(t, wantMethod, typeMethods[target],
				"the member edge carries its type-level parent's Method byte-for-byte")
		}
	})
}
