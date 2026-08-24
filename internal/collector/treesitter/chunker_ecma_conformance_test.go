// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conformsOf returns the declared supertypes captured for one named declaration.
//
// It returns (nil, false) when the declaration carries no type facts at all,
// which is a DIFFERENT answer from "carries facts declaring no supertype" and is
// kept distinguishable so a subtest can say which one it means.
func conformsOf(t *testing.T, res *Result, name string) ([]DeclaredSupertype, bool) {
	t.Helper()
	for _, ch := range res.Chunks {
		if ch.Name != name || ch.ParentName != "" {
			continue
		}
		if ch.TypeFacts == nil {
			return nil, false
		}
		return ch.TypeFacts.Conforms, true
	}
	t.Fatalf("no top-level chunk named %q", name)
	return nil, false
}

// isContract reports the CONTRACT PREDICATE for one named declaration — the
// exact field the declared-conformance emitter gates on.
//
// A declaration carrying NO type facts is not a contract, which is why the nil
// case answers false rather than failing: absent facts and facts with the flag
// clear are the same answer to the emitter, and the emitter is the only reader.
func isContract(t *testing.T, res *Result, name string) bool {
	t.Helper()
	for _, ch := range res.Chunks {
		if ch.Name != name || ch.ParentName != "" {
			continue
		}
		if ch.TypeFacts == nil {
			return false
		}
		return ch.TypeFacts.IsInterface
	}
	t.Fatalf("no top-level chunk named %q", name)
	return false
}

// TestECMAConformanceCapture covers the five syntactic shapes the ECMAScript
// family declares a supertype with, the two capture rules that decline rather
// than guess, and the contract predicate IN BOTH DIRECTIONS.
//
// THE NEGATIVES ARE NOT DECORATION. The positive subtest cannot falsify a
// predicate that returns true for everything, and an always-true predicate would
// convert every `class X extends Y` in a real codebase into an emitted
// conformance edge out of concrete inheritance — which is the exact outcome the
// non-contract rule exists to refuse. Three of the four contract subtests below
// are therefore negative.
func TestECMAConformanceCapture(t *testing.T) {
	t.Run("ts_implements_clause", func(t *testing.T) {
		res := chunkQualFixture(t, "web/impl.ts", "class Svc implements Sink, Other {}\n")
		got, ok := conformsOf(t, res, "Svc")
		require.True(t, ok, "control: the class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{
			{Text: "Sink", Kind: ConformImplements},
			{Text: "Other", Kind: ConformImplements},
		}, got, "every named type in the clause is captured, in source order")
	})

	t.Run("ts_class_extends", func(t *testing.T) {
		// THE KIND ASYMMETRY INSIDE ONE HERITAGE NODE: the extends clause binds an
		// `identifier` while the implements clause binds `type_identifier`s, so an
		// arm assuming one kind for both captures only half of this fixture.
		res := chunkQualFixture(t, "web/both.ts", "class Svc extends Base implements Sink {}\n")
		got, ok := conformsOf(t, res, "Svc")
		require.True(t, ok, "control: the class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{
			{Text: "Base", Kind: ConformExtends},
			{Text: "Sink", Kind: ConformImplements},
		}, got, "extends and implements are captured under DIFFERENT kinds")
	})

	t.Run("ts_interface_extends", func(t *testing.T) {
		// An interface's clause is extends_type_clause, a different node kind from
		// a class's extends_clause, so this is a genuinely separate descent.
		res := chunkQualFixture(t, "web/iface.ts", "interface Ext extends Base2, Base3 {}\n")
		got, ok := conformsOf(t, res, "Ext")
		require.True(t, ok, "control: the interface carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{
			{Text: "Base2", Kind: ConformExtends},
			{Text: "Base3", Kind: ConformExtends},
		}, got)
	})

	t.Run("js_class_heritage_bare", func(t *testing.T) {
		// THE JAVASCRIPT GRAMMAR DECLARES NO extends_clause. The heritage node
		// holds the anonymous `extends` token and the supertype DIRECTLY, so an
		// arm written only against the TypeScript shape captures nothing here
		// while every TypeScript subtest above stays green.
		res := chunkQualFixture(t, "tools/svc.js", "class Svc extends Base {}\n")
		got, ok := conformsOf(t, res, "Svc")
		require.True(t, ok, "control: the javascript class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Base", Kind: ConformExtends}}, got)
	})

	t.Run("exported_class_heritage", func(t *testing.T) {
		// THE UNWRAP'S LOAD-BEARING CASE. The heritage descent reads DIRECT
		// children, so an exported declaration handed over as its export_statement
		// wrapper yields nothing at all — and most real TypeScript classes are
		// exported, which is what makes this the difference between the arm
		// working and the arm being inert on its own corpus.
		res := chunkQualFixture(t, "web/exp.ts", "export class Svc extends Base implements Sink {}\n")
		got, ok := conformsOf(t, res, "Svc")
		require.True(t, ok, "control: the exported class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{
			{Text: "Base", Kind: ConformExtends},
			{Text: "Sink", Kind: ConformImplements},
		}, got)
	})

	t.Run("abstract_class_heritage", func(t *testing.T) {
		res := chunkQualFixture(t, "web/abs.ts", "abstract class Abs implements Sink {}\n"+
			"export abstract class EAbs extends Base {}\n")
		absGot, ok := conformsOf(t, res, "Abs")
		require.True(t, ok, "control: the abstract class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Sink", Kind: ConformImplements}}, absGot)

		eabsGot, ok := conformsOf(t, res, "EAbs")
		require.True(t, ok, "control: the exported abstract class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Base", Kind: ConformExtends}}, eabsGot)
	})

	t.Run("mixin_call_declines", func(t *testing.T) {
		// THE FIXTURE'S CLASS CARRIES NO OTHER HERITAGE, so the decline is the
		// only possible outcome and a bare "nothing captured" cannot be satisfied
		// by some other clause having been read instead.
		res := chunkQualFixture(t, "web/mixin.ts", "class X extends Mixin(Base) {}\n")
		got, _ := conformsOf(t, res, "X")
		assert.Empty(t, got, "a mixin call is not a name and declines rather than guessing")

		// THE BY-NAME NEGATIVES ARE THE POINT. An arm that recorded the callee
		// would write "Mixin"; one that reached past it into the arguments would
		// write "Base". Either bug satisfies a bare emptiness assertion on a
		// fixture whose class also has a real clause, so both are named here.
		for _, c := range got {
			assert.NotEqual(t, "Mixin", c.Text, "the factory is not the supertype")
			assert.NotEqual(t, "Base", c.Text, "the factory's argument is not the declared supertype either")
		}

		// KNOWN-POSITIVE CONTROL in the same run: the identical descent DOES
		// capture a plain name, so the emptiness above is the decline rule firing
		// rather than the whole arm being inert.
		ctl := chunkQualFixture(t, "web/mixin_ctl.ts", "class X extends Base {}\n")
		ctlGot, ok := conformsOf(t, ctl, "X")
		require.True(t, ok, "control: the control class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Base", Kind: ConformExtends}}, ctlGot)
	})

	t.Run("spelling_normalized", func(t *testing.T) {
		// TYPE ARGUMENTS STRIPPED, QUALIFIER RETAINED — the two halves pull in
		// opposite directions, so one assertion catches both mistakes: the whole
		// node text would be "Ns.Other<T>" and the last segment would be "Other".
		res := chunkQualFixture(t, "web/norm.ts", "class X implements Ns.Other<T> {}\n")
		got, ok := conformsOf(t, res, "X")
		require.True(t, ok, "control: the class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Ns.Other", Kind: ConformImplements}}, got)
	})

	t.Run("ts_interface_is_a_contract", func(t *testing.T) {
		res := chunkQualFixture(t, "web/contract.ts", "interface Sink { write(): void; }\n")
		assert.True(t, isContract(t, res, "Sink"),
			"a TypeScript interface is the language's contract construct")
	})

	t.Run("ts_class_is_not_a_contract", func(t *testing.T) {
		res := chunkQualFixture(t, "web/cls.ts", "interface Sink { write(): void; }\nclass Impl { write(): void {} }\n")
		// The positive sits in the SAME fixture, so a predicate stuck at false
		// fails here rather than passing this negative vacuously.
		require.True(t, isContract(t, res, "Sink"), "control: the predicate is not simply always false")
		assert.False(t, isContract(t, res, "Impl"),
			"a concrete class's method IS the callable implementation, so it is not a contract")
	})

	t.Run("ts_abstract_class_is_not_a_contract", func(t *testing.T) {
		res := chunkQualFixture(t, "web/abs2.ts", "interface Sink { write(): void; }\nabstract class Abs { write(): void {} }\n")
		require.True(t, isContract(t, res, "Sink"), "control: the predicate is not simply always false")
		assert.False(t, isContract(t, res, "Abs"),
			"an abstract class may carry implementations, so it is not a contract either")
	})

	t.Run("ts_type_alias_is_not_a_contract", func(t *testing.T) {
		res := chunkQualFixture(t, "web/alias.ts", "interface Sink { write(): void; }\ntype Alias = { a: string };\n")
		require.True(t, isContract(t, res, "Sink"), "control: the predicate is not simply always false")
		assert.False(t, isContract(t, res, "Alias"),
			"a type alias is not a contract, whatever shape it aliases")
	})

	t.Run("tsx_grammar_parity", func(t *testing.T) {
		const src = "export class Svc extends Base implements Sink {}\ninterface Ext extends Base2 {}\n"
		tsRes := chunkQualFixture(t, "web/parity.ts", src)
		tsxRes := chunkQualFixture(t, "web/parity.tsx", src)

		// Compared against a FIXTURE-DERIVED expectation rather than against each
		// other: two arms that both captured nothing are still equal.
		wantClass := []DeclaredSupertype{
			{Text: "Base", Kind: ConformExtends},
			{Text: "Sink", Kind: ConformImplements},
		}
		wantIface := []DeclaredSupertype{{Text: "Base2", Kind: ConformExtends}}

		tsClass, ok := conformsOf(t, tsRes, "Svc")
		require.True(t, ok, "control: the typescript class carries type facts")
		tsxClass, ok := conformsOf(t, tsxRes, "Svc")
		require.True(t, ok, "control: the tsx class carries type facts")
		assert.Equal(t, wantClass, tsClass)
		assert.Equal(t, wantClass, tsxClass)

		tsIface, _ := conformsOf(t, tsRes, "Ext")
		tsxIface, _ := conformsOf(t, tsxRes, "Ext")
		assert.Equal(t, wantIface, tsIface)
		assert.Equal(t, wantIface, tsxIface)

		assert.True(t, isContract(t, tsxRes, "Ext"), "tsx marks an interface as a contract too")
	})
}
