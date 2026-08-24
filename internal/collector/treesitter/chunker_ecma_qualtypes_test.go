// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ecmaChunkType returns the chunk type recorded for the declaration of one name.
func ecmaChunkType(t *testing.T, res *Result, name string) string {
	t.Helper()
	for _, ch := range res.Chunks {
		if ch.Name == name {
			return ch.ChunkType
		}
	}
	t.Fatalf("no chunk named %q", name)
	return ""
}

// TestTSQualifierTypes covers the typescript and tsx arm's four binding routes,
// the non-arrow suppression rule, and the two capture shapes a position-based
// reading of the grammar would get wrong.
func TestTSQualifierTypes(t *testing.T) {
	t.Run("this_receiver", func(t *testing.T) {
		const src = `class Svc {
  run(p: Req) {
    this.dep.go();
    p.use();
  }
}
`
		res := chunkQualFixture(t, "pkg/recv.ts", src)
		got := qualTypesFor(t, res, "Svc.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Svc"}, got["this"], "this binds to the enclosing class")
	})

	t.Run("annotated_param", func(t *testing.T) {
		const src = `class Svc {
  run(p: Req, q?: Opt, n: number, u: A | B) {
    p.use();
  }
}
`
		res := chunkQualFixture(t, "pkg/param.ts", src)
		got := qualTypesFor(t, res, "Svc.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")

		assert.Equal(t, QualType{Text: "Req"}, got["p"], "a required parameter binds its annotation")
		assert.Equal(t, QualType{Text: "Opt"}, got["q"], "an optional parameter binds its annotation")

		// THE DECLINES ARE THE POINT OF THE ALLOWLIST. A predefined type names no
		// declaration this repository could hold, and a union names more than one
		// — binding either would put a qualifier on a type its value may not have.
		_, boundPredefined := got["n"]
		_, boundUnion := got["u"]
		assert.False(t, boundPredefined, "a predefined_type declines")
		assert.False(t, boundUnion, "a union_type declines")
	})

	t.Run("annotated_local", func(t *testing.T) {
		const src = `class Svc {
  run() {
    const a: Local = mk();
    const g: Box<Inner> = mk();
    const s: string = "x";
    a.use();
    g.use();
    s.length;
  }
}
`
		res := chunkQualFixture(t, "pkg/local.ts", src)
		got := qualTypesFor(t, res, "Svc.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")

		assert.Equal(t, QualType{Text: "Local"}, got["a"], "an annotated local binds its annotation, not its initialiser")
		assert.Equal(t, QualType{Text: "Box"}, got["g"], "a generic instantiation binds its base, type arguments stripped")

		_, boundPredefined := got["s"]
		assert.False(t, boundPredefined, "a predefined_type annotation declines")
	})

	t.Run("new_expression_local", func(t *testing.T) {
		const src = `class Svc {
  run() {
    const b = new Thing();
    const c = new ns.Other();
    b.use();
    c.use();
  }
}
`
		res := chunkQualFixture(t, "pkg/new.ts", src)
		got := qualTypesFor(t, res, "Svc.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")

		// A constructor NAMES the type in ECMAScript, so this is a direct binding
		// with FromCall false — not a call whose result must be looked up.
		assert.Equal(t, QualType{Text: "Thing"}, got["b"], "new binds the constructed type directly")

		_, boundQualified := got["c"]
		assert.False(t, boundQualified, "a member_expression constructor declines rather than taking a second hop")
	})

	t.Run("function_form_suppresses_this", func(t *testing.T) {
		const src = `class Svc {
  run(p: Req) {
    const f = function () { return 1; };
    this.dep.go();
    p.use();
  }
}
`
		res := chunkQualFixture(t, "pkg/suppress.ts", src)
		got := qualTypesFor(t, res, "Svc.run")
		// KNOWN-POSITIVE CONTROL. Without it, an arm that bound nothing at all
		// would satisfy the absence below vacuously.
		require.NotEmpty(t, got, "control: the declaration still bound its other qualifiers")
		assert.Equal(t, QualType{Text: "Req"}, got["p"], "control: the parameter route is unaffected by the suppression")

		_, boundThis := got["this"]
		assert.False(t, boundThis, "a non-arrow function form rebinds this, so the whole declaration declines it")
	})

	t.Run("arrow_form_does_not_suppress_this", func(t *testing.T) {
		const src = `class Svc {
  run(p: Req) {
    const f = () => 1;
    this.dep.go();
    p.use();
  }
}
`
		res := chunkQualFixture(t, "pkg/arrow.ts", src)
		got := qualTypesFor(t, res, "Svc.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Svc"}, got["this"], "an arrow function is transparent to this and must not suppress it")
	})

	t.Run("nested_class_this_does_not_bind_outward", func(t *testing.T) {
		// A NESTED CLASS REBINDS `this` FOR ITS OWN MEMBERS, so a declaration
		// holding a class expression cannot attribute a `this` inside it to the
		// class that encloses the declaration. Before the class arm was added to
		// the suppression, the `const Inner = class {...}` declaration bound this
		// to Outer — a WRONG TARGET, since every this.x written inside the class
		// expression would resolve against Outer's members.
		const src = `class Outer {
  make(): void {
    const Inner = class { run(): void { this.go(); } };
  }
}

class Plain {
  go(p: Req): void {
    this.dep.use();
    p.use();
  }
}
`
		res := chunkQualFixture(t, "web/nested.ts", src)

		// THE CONTROL IS IN THE SAME RUN: a method with no nested class still
		// binds its receiver, so the absence below is the suppression firing
		// rather than the receiver route having stopped working.
		plain := qualTypesFor(t, res, "Plain.go")
		require.NotEmpty(t, plain, "control: an unnested method still binds qualifiers")
		assert.Equal(t, QualType{Text: "Plain"}, plain["this"],
			"control: a method with no nested class binds this to its own class")

		inner := qualTypesFor(t, res, "make.Inner")
		_, boundThis := inner["this"]
		assert.False(t, boundThis,
			"a declaration holding a class expression must not bind this to the class outside it")
	})

	t.Run("parameter_property_modifier", func(t *testing.T) {
		// THE REGRESSION THIS PINS: required_parameter's named children for
		// `private store: Store` are (accessibility_modifier, identifier,
		// type_annotation), so a rule reading named child 0 as the name binds
		// "private" to Store. `readonly` shifts nothing, because it arrives as an
		// anonymous token — so a fixed offset cannot correct the first case
		// without breaking the second.
		const src = `class Svc {
  constructor(private store: Store, readonly other: Other, plain: Plain) {
    this.store.get();
  }
}
`
		res := chunkQualFixture(t, "pkg/paramprop.ts", src)
		got := qualTypesFor(t, res, "Svc.constructor")
		require.NotEmpty(t, got, "control: the constructor bound qualifiers at all")

		assert.Equal(t, QualType{Text: "Store"}, got["store"], "an accessibility-modified parameter binds under its own name")
		assert.Equal(t, QualType{Text: "Other"}, got["other"], "a readonly parameter binds under its own name")
		assert.Equal(t, QualType{Text: "Plain"}, got["plain"], "control: an unmodified parameter binds the same way")

		_, boundModifier := got["private"]
		assert.False(t, boundModifier, "the accessibility modifier is not the parameter's name")
	})

	t.Run("exported_declaration_unwraps", func(t *testing.T) {
		// TWO ASSERTIONS, AND ONLY THE FIRST IS SENSITIVE TO THE UNWRAP ITSELF.
		// The chunk type comes from resolveChunkType, which delegates to
		// unwrapExportedDecl, so a helper that failed to resolve an export would
		// record this declaration as an export_statement. The qualifier map below
		// it is a regression guard on the exported-declaration path rather than a
		// second proof of the unwrap: the arm's walk is recursive, so it reaches a
		// parameter through an export_statement wrapper either way. Where the
		// unwrap is genuinely load-bearing is a DIRECT-child descent — the type
		// facts arm's heritage read — and that is gated with its own test.
		const src = `export class Svc {
  run(p: Req) {
    p.use();
  }
}
`
		res := chunkQualFixture(t, "pkg/exported.ts", src)
		assert.Equal(t, "class_declaration", ecmaChunkType(t, res, "Svc"),
			"an exported declaration resolves to the declaration it wraps, not to export_statement")

		got := qualTypesFor(t, res, "Svc.run")
		require.NotEmpty(t, got, "control: the exported declaration's member bound qualifiers")
		assert.Equal(t, QualType{Text: "Req"}, got["p"])
		assert.Equal(t, QualType{Text: "Svc"}, got["this"], "the receiver ascent reaches through the export wrapper")
	})

	t.Run("tsx_grammar_parity", func(t *testing.T) {
		// THE TSX TABLE'S OWN PROOF. tsx is a separate grammar numbering the same
		// kind names differently, so a tsx arm reading the typescript table would
		// classify every node as Other and bind nothing — and one built for a kind
		// tsx does not declare would panic here rather than return a wrong answer.
		const src = `class Svc {
  run(p: Req) {
    const b = new Thing();
    this.dep.go();
    p.use();
    b.use();
  }
}
`
		tsRes := chunkQualFixture(t, "pkg/parity.ts", src)
		tsxRes := chunkQualFixture(t, "pkg/parity.tsx", src)

		tsGot := qualTypesFor(t, tsRes, "Svc.run")
		tsxGot := qualTypesFor(t, tsxRes, "Svc.run")

		// Compared against a FIXTURE-DERIVED expectation rather than against each
		// other: two maps that both lost the same entries are still equal, so an
		// equality alone would go green on two arms that had both stopped working.
		want := map[string]QualType{
			"this": {Text: "Svc"},
			"p":    {Text: "Req"},
			"b":    {Text: "Thing"},
		}
		assert.Equal(t, want, tsGot, "typescript binds all three routes")
		assert.Equal(t, want, tsxGot, "tsx binds all three routes through its own symbol table")
	})
}

// TestJSQualifierTypes covers the javascript arm, which has the receiver and
// constructor routes only, and records the annotation absence as an asserted
// fact rather than an untested silence.
func TestJSQualifierTypes(t *testing.T) {
	t.Run("this_receiver", func(t *testing.T) {
		const src = `class Svc {
  run() {
    const b = new Thing();
    this.dep.go();
    b.use();
  }
}
`
		res := chunkQualFixture(t, "pkg/recv.js", src)
		got := qualTypesFor(t, res, "Svc.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Svc"}, got["this"], "this binds to the enclosing class")
	})

	t.Run("new_expression_local", func(t *testing.T) {
		const src = `class Svc {
  run() {
    const b = new Thing();
    const c = new ns.Other();
    b.use();
    c.use();
  }
}
`
		res := chunkQualFixture(t, "pkg/new.js", src)
		got := qualTypesFor(t, res, "Svc.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Thing"}, got["b"], "new binds the constructed type directly")

		_, boundQualified := got["c"]
		assert.False(t, boundQualified, "a member_expression constructor declines")
	})

	t.Run("jsdoc_annotation_not_parsed", func(t *testing.T) {
		// A RECORDED LIMITATION, NOT AN ABSENCE NOBODY CHECKED. JSDoc arrives as
		// one opaque comment token and comment chunks are dropped from the graph
		// entirely, so a JSDoc-annotated parameter binds nothing. The control
		// beside it is what makes the absence readable: the same declaration binds
		// its constructor local, so the nil answer is the annotation route being
		// genuinely absent rather than the arm being unregistered.
		const src = `class Svc {
  /** @param {Foo} x */
  run(x) {
    const b = new Thing();
    x.use();
    b.use();
  }
}
`
		res := chunkQualFixture(t, "pkg/jsdoc.js", src)
		got := qualTypesFor(t, res, "Svc.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Thing"}, got["b"], "control: the constructor route works in the same declaration")

		_, boundFromJSDoc := got["x"]
		assert.False(t, boundFromJSDoc, "JSDoc is not parsed, so an annotated parameter binds nothing")
	})

	t.Run("exported_declaration_unwraps", func(t *testing.T) {
		const src = `export class Svc {
  run() {
    const b = new Thing();
    this.dep.go();
    b.use();
  }
}
`
		res := chunkQualFixture(t, "pkg/exported.js", src)
		assert.Equal(t, "class_declaration", ecmaChunkType(t, res, "Svc"),
			"an exported declaration resolves to the declaration it wraps, not to export_statement")

		got := qualTypesFor(t, res, "Svc.run")
		require.NotEmpty(t, got, "control: the exported declaration's member bound qualifiers")
		assert.Equal(t, QualType{Text: "Svc"}, got["this"], "the receiver ascent reaches through the export wrapper")
	})
}
