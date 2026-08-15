// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// nodeIDSet returns every node ID the populate pass produced.
func nodeIDSet(res PopulateResult) map[string]bool {
	ids := make(map[string]bool, len(res.Nodes))
	for _, n := range res.Nodes {
		ids[n.Id] = true
	}
	return ids
}

// idsWithPrefix returns the node IDs starting with prefix — used for the
// collision shapes, where the astPathHash suffix is not predictable.
func idsWithPrefix(res PopulateResult, prefix string) []string {
	var out []string
	for _, n := range res.Nodes {
		if strings.HasPrefix(n.Id, prefix) {
			out = append(out, n.Id)
		}
	}
	return out
}

const tsShapesSrc = `export interface Point { x: number; y: number; }

export class Box {
  constructor(private w: number) {}
  area(): number { return 1; }
  get width(): number { return this.w; }
  set width(v: number) { this.w = v; }
  static make(): Box { return new Box(1); }
  async load(): Promise<void> {}
  [Symbol.iterator]() {}
  prop: number = 3;
}

export class Circle {
  area(): number { return 2; }
}

function make(): Box { return new Box(1); }

export function build(p: Point): Box { return make(); }
`

func TestPopulate_TypeScriptEdgesSurvive(t *testing.T) {
	res := populateFixture(t, []fixtureFile{{path: "web/shapes.ts", src: tsShapesSrc}})
	ids := nodeIDSet(res)

	// Five top-level declarations, six Box members and one Circle member.
	fileContains := edgesFrom(res, kgtypes.EdgeContains, "web/shapes.ts")
	assert.Len(t, fileContains, 12)

	// CLASS → MEMBER CONTAINS, counted from fixture-derived constants.
	assert.Len(t, edgesFrom(res, kgtypes.EdgeContains, "web/shapes.ts:Box"), 6)
	boxMembers := edgesFrom(res, kgtypes.EdgeContains, "web/shapes.ts:Circle")
	assert.Equal(t, []string{"web/shapes.ts:Circle.area"}, boxMembers)

	// CROSS-CLASS same name must NOT collide: distinct parents, no suffix.
	assert.True(t, ids["web/shapes.ts:Box.area"])
	assert.True(t, ids["web/shapes.ts:Circle.area"])

	// WITHIN-CLASS same name MUST collide: the getter and setter both named
	// width. The cross-class pair above is this assertion's known positive,
	// proving the suffix is applied selectively rather than everywhere.
	widths := idsWithPrefix(res, "web/shapes.ts:Box.width")
	assert.Len(t, widths, 2)
	assert.NotEqual(t, widths[0], widths[1])
	for _, id := range widths {
		assert.Contains(t, id, "#")
	}

	// Parent qualification is load-bearing: the static and the top-level
	// function share the name "make" and would collide on one ID without it.
	assert.True(t, ids["web/shapes.ts:Box.make"])
	assert.True(t, ids["web/shapes.ts:make"])

	// EXCLUDED member kinds, with the twelve positives above as the control.
	assert.False(t, ids["web/shapes.ts:Box.prop"])
	for id := range ids {
		assert.NotContains(t, id, "Symbol.iterator")
	}

	// The bare callee resolves to the top-level function, not the static.
	assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/shapes.ts:build", "web/shapes.ts:make"))
	assert.False(t, hasEdge(res, kgtypes.EdgeCalls, "web/shapes.ts:build", "web/shapes.ts:Box.make"))

	// USES_TYPE, including one out of a MEMBER chunk — no total is asserted,
	// because the query captures every type_identifier including self-names.
	assert.True(t, hasEdge(res, kgtypes.EdgeUsesType, "web/shapes.ts:build", "web/shapes.ts:Box"))
	assert.True(t, hasEdge(res, kgtypes.EdgeUsesType, "web/shapes.ts:build", "web/shapes.ts:Point"))
	assert.True(t, hasEdge(res, kgtypes.EdgeUsesType, "web/shapes.ts:Box.make", "web/shapes.ts:Box"))
}

const jsShapesSrc = `export class Box {
  constructor(w) { this.w = w; }
  area() { return 1; }
  get width() { return this.w; }
  set width(v) { this.w = v; }
  static make() { return new Box(1); }
  async load() {}
  [Symbol.iterator]() {}
  #secret() { return 1; }
  prop = 3;
}

export class Circle {
  area() { return 2; }
}

function make() { return new Box(1); }

export function build(p) { return make(); }
`

func TestPopulate_JavaScriptEdgesSurvive(t *testing.T) {
	res := populateFixture(t, []fixtureFile{{path: "web/shapes.js", src: jsShapesSrc}})
	ids := nodeIDSet(res)

	// Four top-level declarations (no interface in the JS fixture), six Box
	// members and one Circle member.
	assert.Len(t, edgesFrom(res, kgtypes.EdgeContains, "web/shapes.js"), 11)
	assert.Len(t, edgesFrom(res, kgtypes.EdgeContains, "web/shapes.js:Box"), 6)
	assert.Equal(t, []string{"web/shapes.js:Circle.area"},
		edgesFrom(res, kgtypes.EdgeContains, "web/shapes.js:Circle"))

	assert.True(t, ids["web/shapes.js:Box.area"])
	assert.True(t, ids["web/shapes.js:Circle.area"])

	widths := idsWithPrefix(res, "web/shapes.js:Box.width")
	assert.Len(t, widths, 2)
	assert.NotEqual(t, widths[0], widths[1])

	assert.True(t, ids["web/shapes.js:Box.make"])
	assert.True(t, ids["web/shapes.js:make"])

	// EXCLUDED, including the private-name member that only JavaScript has here.
	assert.False(t, ids["web/shapes.js:Box.prop"])
	for id := range ids {
		assert.NotContains(t, id, "Symbol.iterator")
		assert.NotContains(t, id, "secret")
	}

	assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/shapes.js:build", "web/shapes.js:make"))
	assert.False(t, hasEdge(res, kgtypes.EdgeCalls, "web/shapes.js:build", "web/shapes.js:Box.make"))

	// ZERO USES_TYPE: jsQueries sets TypeRefs empty, so extractTypeRefEdges
	// returns nil for JavaScript by construction. The KNOWN-POSITIVE CONTROL
	// for this zero is TestPopulate_TypeScriptEdgesSurvive's surviving
	// USES_TYPE assertions — the same mechanism driven with a non-empty query.
	// The eleven CONTAINS above prove only that the fixture ran.
	assert.Equal(t, 0, countEdges(res, kgtypes.EdgeUsesType))
}

// The polyglot fixture. Every detail of the layout is load-bearing and must
// not be tidied: helper lives in a DIFFERENT Go package from its caller, which
// no longer resolves at all now that a bare name is looked up in its own scope
// unit (see the re-derived assertion below); the Python file's directory
// basename is deliberately "svc", equal to the Go package name, so an
// unprefixed namespace would put Python's symbols on the exact keys Go's
// same-package lookup reads; and local exists in both languages so the
// same-package hijack has a target.
var polyglotGoFiles = []fixtureFile{
	{path: "a/svc.go", src: "package svc\n\nfunc run() string { return helper() + local() }\n"},
	{path: "a/local.go", src: "package svc\n\nfunc local() string { return \"go-local\" }\n"},
	{path: "b/util.go", src: "package util\n\nfunc helper() string { return \"go-helper\" }\n"},
}

var polyglotPyFile = fixtureFile{
	path: "svc/thing.py",
	src:  "def helper():\n    return \"py\"\n\n\ndef local():\n    return \"py\"\n",
}

func TestPopulate_PolyglotGoNotDegraded(t *testing.T) {
	// The declaration index cannot represent a collision at all, so no writer
	// can take another's slot. Both orderings are still run and still required
	// to agree: asserting only one arrangement would leave the gate betting on
	// it, and a map-keyed fixture would randomize it.
	orders := map[string][]fixtureFile{
		"python_last":  append(append([]fixtureFile{}, polyglotGoFiles...), polyglotPyFile),
		"python_first": append([]fixtureFile{polyglotPyFile}, polyglotGoFiles...),
	}
	for name, files := range orders {
		t.Run(name, func(t *testing.T) {
			res := populateFixture(t, files)

			// RE-DERIVED. This used to require the cross-package call to bind,
			// and it bound only because the resolver searched the whole
			// language partition by bare name. A Go bare name is now looked up
			// in its OWN scope unit — the caller's directory — and helper is
			// declared in another one, so it lands External. Go's own rule
			// agrees: `helper()` unqualified never refers to another package.
			// Restoring this edge means binding it through the IMPORT, which
			// is what a registered BindsResolver arm will do.
			assert.False(t, hasEdge(res, kgtypes.EdgeCalls, "a/svc.go:run", "b/util.go:helper"),
				"an unqualified Go call must not reach another package by name")

			// Same-package: the call that SHOULD bind, and the one a same-named
			// Python declaration could have hijacked. It is also the
			// known-positive control for the negative above — without it, a
			// build that resolved nothing at all would satisfy that assertion.
			assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "a/svc.go:run", "a/local.go:local"),
				"the Go same-package call must not be hijacked")

			// No Go endpoint may point into the Python file.
			for _, e := range res.Edges {
				if strings.HasPrefix(e.FromId, "a/") || strings.HasPrefix(e.FromId, "b/") {
					assert.False(t, strings.HasPrefix(e.ToId, "svc/thing.py"),
						"Go edge %s -> %s crossed into the Python file", e.FromId, e.ToId)
				}
			}

			// KNOWN-POSITIVE CONTROL. Without it the assertion above passes
			// vacuously whenever Python emits nothing — which is exactly the
			// pre-fix behavior this plan exists to end, so the fixture would
			// certify a broken build as safe.
			pyContains := edgesFrom(res, kgtypes.EdgeContains, "svc/thing.py")
			assert.Contains(t, pyContains, "svc/thing.py:helper")
			assert.Contains(t, pyContains, "svc/thing.py:local")
		})
	}
}

const goSvcSrc = `package svc

type Animal struct{ name string }

func (a Animal) Speak() string { return barkSound() }

func barkSound() string { return "woof" }

func run() string {
	a := Animal{name: "dog"}
	return a.Speak() + barkSound()
}
`

func TestPopulate_GoControlPackageNamespace(t *testing.T) {
	// The directory ("pkg") deliberately differs from the package name
	// ("svc"), so a namespace taken from the path instead of the clause is
	// falsifiable rather than invisible.
	res := populateFixture(t, []fixtureFile{{path: "pkg/svc.go", src: goSvcSrc}})

	assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "pkg/svc.go:run", "pkg/svc.go:barkSound"))
	assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "pkg/svc.go:Animal.Speak", "pkg/svc.go:barkSound"))
	assert.Contains(t, edgesFrom(res, kgtypes.EdgeContains, "pkg/svc.go"), "pkg/svc.go:Animal.Speak")

	// NO-DOUBLE-EMISSION PIN. Asserted by COUNT, not membership: an
	// implementer who ADDS a parent branch instead of REPLACING the receiver
	// branch emits this identical edge twice and every membership assertion
	// still passes.
	animalContains := edgesFrom(res, kgtypes.EdgeContains, "pkg/svc.go:Animal")
	assert.Equal(t, []string{"pkg/svc.go:Animal.Speak"}, animalContains)
	assert.Len(t, animalContains, 1)

	// Plain functions have no parent, so they emit no parent CONTAINS.
	assert.Empty(t, edgesFrom(res, kgtypes.EdgeContains, "pkg/svc.go:run"))
	assert.Empty(t, edgesFrom(res, kgtypes.EdgeContains, "pkg/svc.go:barkSound"))
}

// jsMemberCallSrc pits a class's STATIC member against a top-level function of
// the same name, so the two spellings of the javascript CALLS capture resolve
// to DIFFERENT declarations and the difference is observable as an edge target.
//
// `Box.make()` is a member expression. Captured as the whole expression it
// arrives at resolution as "Box.make", splits at its last dot, and binds
// through the qualified-parent rung to the static member. Captured as the
// property identifier alone it arrives as the bare "make", cannot reach any
// qualified rung at all, and binds through the own-scope rung to the top-level
// function — a WRONG edge that no assertion in this package caught before.
const jsMemberCallSrc = `import * as ns from './other';

export class Box {
  static make() { return 1; }
}

function make() { return 2; }

export function run() { return Box.make(); }
`

// TestPopulate_JavaScriptMemberCallQualified is the reproduction AND the
// regression guard for the javascript member-expression capture.
//
// EXPECTED FAILURE AGAINST THE UNFIXED TREE, so a fixture error is
// distinguishable from the real red: with the property-identifier capture the
// FIRST assertion fails (no edge to Box.make, because the callee text is the
// bare "make") and the SECOND fails too (an edge to the top-level make exists).
// Any other failure shape means the fixture is wrong, not the query.
func TestPopulate_JavaScriptMemberCallQualified(t *testing.T) {
	res := populateFixture(t, []fixtureFile{{path: "web/membercall.js", src: jsMemberCallSrc}})

	assert.True(t, hasEdge(res, kgtypes.EdgeCalls, "web/membercall.js:run", "web/membercall.js:Box.make"),
		"a qualified member call binds to the member its qualifier names")
	assert.False(t, hasEdge(res, kgtypes.EdgeCalls, "web/membercall.js:run", "web/membercall.js:make"),
		"and never to the same-named top-level function, which is what the bare "+
			"property-identifier capture bound it to")

	// KNOWN-POSITIVE CONTROL for the two assertions above: both would also hold
	// if this fixture emitted no CALLS edges whatsoever.
	assert.NotEmpty(t, edgesFrom(res, kgtypes.EdgeCalls, "web/membercall.js:run"),
		"the fixture must actually emit a call edge")
}
