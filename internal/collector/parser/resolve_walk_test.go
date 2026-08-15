// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// refEdgeFrom returns the one CALLS edge with the given verbatim target that
// the referencing fixture file emitted, so a test can resolve the REAL carrier
// the chunker built rather than a hand-written stand-in.
//
// THE FILE IS FIXED rather than a parameter: every fixture in this package puts
// the referencing declarations in app/main.go, so a file argument would carry
// no information (the unparam linter says so). A fixture that needs a second
// referencing file reintroduces the parameter.
const refEdgeFile = "app/main.go"

func refEdgeFrom(t *testing.T, results []*treesitter.Result, target string) *treesitter.Edge {
	t.Helper()
	for _, r := range results {
		if r.FilePath != refEdgeFile {
			continue
		}
		for i := range r.Edges {
			e := &r.Edges[i]
			if e.ToID == target && e.Type == treesitter.EdgeCalls {
				return e
			}
		}
	}
	t.Fatalf("no CALLS edge to %q in %s", target, refEdgeFile)
	return nil
}

// importedHelper is the two-scope corpus both import-rule cases resolve over:
// a callee in one Go package and a caller in another, plus a DECOY of the same
// name in the caller's own scope. The decoy is what makes each case
// falsifiable — if the import rule does not fire, the ladder falls through to
// its own scope and binds the decoy instead, which the assertions can tell apart.
var importedHelper = []fixtureFile{
	{path: "lib/lib.go", src: "" +
		"package lib\n\nfunc Helper() int {\n\treturn 1\n}\n"},
	{path: "app/local.go", src: "" +
		"package main\n\nfunc Helper() int {\n\treturn 9\n}\n"},
}

// TestResolveRefRuleLadder walks every rung. Each case asserts the STATUS, the
// RULE that fired, and that rule constant's string value — the value is what a
// later reader of a resolution audit sees, so a silent rename of the constant's
// text is a break even when the identifier still compiles.
func TestResolveRefRuleLadder(t *testing.T) {
	// R1 and R4 are inert in this ticket: no language ships a BindsResolver
	// arm. Both cases install a test-local arm through the real registry, so
	// the seam the dependent per-language tickets will use is exercised here
	// rather than assumed, and both restore the registry on the way out.
	t.Run("R1_qualified_import", func(t *testing.T) {
		treesitter.RegisterBindsResolver(treesitter.LangGo,
			func(_ *treesitter.RepoContext, _ map[string]*treesitter.Result, self *treesitter.Result) treesitter.BindsResult {
				if self.FilePath == "app/main.go" {
					return treesitter.BindsResult{Binds: map[string]treesitter.Bind{"lib": {Scope: "dir:lib"}}}
				}
				return treesitter.BindsResult{}
			})
		// RESTORE, NEVER DELETE — Go ships a real arm registered at init, and
		// unregistering it here would disarm it for every later test.
		t.Cleanup(func() { treesitter.RegisterGoBindsResolver() })

		files := append(append([]fixtureFile{}, importedHelper...), fixtureFile{
			path: "app/main.go", src: "" +
				"package main\n\nimport \"example.com/lib\"\n\nfunc Run() int {\n\treturn lib.Helper()\n}\n"})
		results := chunkFixture(t, files)
		fillBinds(&treesitter.RepoContext{}, results)
		ix := indexResults(t, results)
		e := refEdgeFrom(t, results, "lib.Helper")
		require.NotNil(t, e.Ref)
		require.Equal(t, "dir:lib", e.Ref.Binds["lib"].Scope, "the registered arm must reach the reference site")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleQualifiedImport, got.Rule)
		assert.Equal(t, "qualified-import", string(got.Rule))
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "lib/lib.go:Helper", got.Candidates[0].NodeID,
			"the import binds the reference into the IMPORTED scope, not the caller's own")
	})

	t.Run("R2X_external_qualifier_terminates", func(t *testing.T) {
		// The one rung that removes a WRONG-EDGE class rather than an index
		// gap. `ext.Helper()` in a package that ALSO declares Helper would
		// otherwise miss R1 (the External target has no indexed declarations),
		// miss R2 (no local container named ext), and fall into R3, which would
		// emit an open-set dynamic edge to the LOCAL Helper — a candidate known
		// not to be the referent.
		//
		// THE ARM IS REGISTERED ON A PARENTED DECLARATION'S REFERENCE, which is
		// the catcher for the fill-in-place rule: a parented reference site is
		// a by-value copy taken during chunking, so a pass that ASSIGNED a
		// fresh map would leave this site's Binds nil and this case would fall
		// through to R3 with no compile error.
		treesitter.RegisterBindsResolver(treesitter.LangGo,
			func(_ *treesitter.RepoContext, _ map[string]*treesitter.Result, self *treesitter.Result) treesitter.BindsResult {
				if self.FilePath == "app/main.go" {
					// The arm records its best-effort Scope and nothing else.
					// Externality is the INDEX's to decide: no file in this
					// fixture declares anything under dir:vendorless, so the
					// scope set does not hold it.
					return treesitter.BindsResult{Binds: map[string]treesitter.Bind{"ext": {Scope: "dir:vendorless"}}}
				}
				return treesitter.BindsResult{}
			})
		// RESTORE, NEVER DELETE — Go ships a real arm registered at init, and
		// unregistering it here would disarm it for every later test.
		t.Cleanup(func() { treesitter.RegisterGoBindsResolver() })

		files := []fixtureFile{
			{path: "app/local.go", src: "" +
				"package main\n\nfunc Helper() int {\n\treturn 9\n}\n"},
			{path: "app/main.go", src: "" +
				"package main\n\ntype Runner struct{}\n\nfunc (r Runner) Run() int {\n\treturn ext.Helper()\n}\n"},
		}
		results := chunkFixture(t, files)
		fillBinds(&treesitter.RepoContext{}, results)
		ix := indexResults(t, results)
		e := refEdgeFrom(t, results, "ext.Helper")
		require.NotNil(t, e.Ref)
		require.NotEmpty(t, e.Ref.Parent, "the fixture's reference must come from a PARENTED declaration")
		require.Equal(t, "dir:vendorless", e.Ref.Binds["ext"].Scope,
			"the arm's bind must reach the PARENTED site — fill-in-place, never assign")
		require.False(t, ix.hasScope("dir:vendorless"),
			"the fixture's bind must name a scope the index genuinely does not hold")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefExternal, got.Status)
		assert.Equal(t, RuleExternalQualifier, got.Rule)
		assert.Equal(t, "external-qualifier", string(got.Rule))
		assert.Empty(t, got.Candidates, "termination emits no edge at all")

		// KNOWN-POSITIVE CONTROL: the SAME reference with NO bind recorded for
		// the qualifier falls through to R3 and manufactures exactly the edge
		// R2X exists to remove — an open-set dynamic edge to the LOCAL Helper.
		// Without this, a ladder that returned External for every qualified
		// reference would pass.
		unbound := *e.Ref
		unbound.Binds = map[string]treesitter.Bind{}
		ctrl := resolveRef(ix, &unbound, e.ToID)
		assert.Equal(t, RefDynamic, ctrl.Status)
		assert.Equal(t, RuleDynamicScope, ctrl.Rule)
		assert.NotEmpty(t, ctrl.Candidates,
			"control: with no bind recorded, the reference reaches the local Helper")
	})

	t.Run("R4_unqualified_import", func(t *testing.T) {
		// The rule the seam contract cannot live without: TypeScript and
		// JavaScript references arrive UNQUALIFIED, so without a bare-name
		// Binds consultation an imported foo() reaches R6, misses, and lands
		// External with no rule able to recover it.
		//
		// The fixture is GO — a dot import — because R4's placement BEFORE the
		// bare-name rules is only correct for languages whose module system
		// forbids an import and a local of one name from coexisting. Writing
		// this case in python or ruby would encode a precedence that is wrong
		// for those languages.
		treesitter.RegisterBindsResolver(treesitter.LangGo,
			func(_ *treesitter.RepoContext, _ map[string]*treesitter.Result, self *treesitter.Result) treesitter.BindsResult {
				if self.FilePath == "app/main.go" {
					return treesitter.BindsResult{Binds: map[string]treesitter.Bind{"Helper": {Scope: "dir:lib"}}}
				}
				return treesitter.BindsResult{}
			})
		// RESTORE, NEVER DELETE — Go ships a real arm registered at init, and
		// unregistering it here would disarm it for every later test.
		t.Cleanup(func() { treesitter.RegisterGoBindsResolver() })

		files := append(append([]fixtureFile{}, importedHelper...), fixtureFile{
			path: "app/main.go", src: "" +
				"package main\n\nimport . \"example.com/lib\"\n\nfunc Run() int {\n\treturn Helper()\n}\n"})
		results := chunkFixture(t, files)
		fillBinds(&treesitter.RepoContext{}, results)
		ix := indexResults(t, results)
		e := refEdgeFrom(t, results, "Helper")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleUnqualifiedImport, got.Rule)
		assert.Equal(t, "unqualified-import", string(got.Rule))
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "lib/lib.go:Helper", got.Candidates[0].NodeID,
			"a bare name an import bound resolves in the BOUND scope, not the caller's own")
	})

	t.Run("ecma_constructor_binds_import", func(t *testing.T) {
		// THE CONSTRUCTOR'S OWN RESOLUTION PROOF. The Calls query captures
		// `new Alpha()` as the bare callee `Alpha`, which carries no separator,
		// so it takes the bare path and reaches R4 — the same rung an ordinary
		// imported function uses. What makes this case worth its own entry is
		// that THE CONSTRUCTOR IS THE ONLY USE of the imported name: nothing
		// else in app/main.ts mentions Alpha, so a build where constructor
		// references are not captured emits no reference to resolve at all.
		//
		// The imported symbol is a TOP-LEVEL exported class deliberately. R4
		// builds its declKey without the Parent field, so it binds only
		// unparented declarations; a member class would need the parent-aware
		// lookup a sibling ticket owns, and nothing here asserts one.
		//
		// NO ARM IS REGISTERED HERE — chunker_binds.go's init already registered
		// the ECMAScript arms for the whole test binary, so registering would
		// shadow the arm under test.
		//
		// THE REPO-CARRYING HARNESS IS REQUIRED, not a preference. The
		// ECMAScript arm is a module resolver: it turns '../lib/alpha' into a
		// file by consulting the repository, so the empty RepoContext
		// resolveFixtureRef supplies leaves it binding nothing at all.
		ix, e := resolveRepoFixtureRef(t, []fixtureFile{
			{path: "lib/alpha.ts", src: "export class Alpha {\n  run(): number {\n    return 1;\n  }\n}\n"},
			{path: "app/main.ts", src: "" +
				"import { Alpha } from '../lib/alpha';\n\n" +
				"export function build(): number {\n  return new Alpha().run();\n}\n"},
		}, "app/main.ts", "Alpha")

		require.NotNil(t, e.Ref)
		require.Equal(t, "file:lib/alpha.ts", e.Ref.Binds["Alpha"].Scope,
			"the ECMAScript arm resolves the specifier to the imported file")

		got := resolveRef(ix, e.Ref, e.ToID)
		assert.Equal(t, RefBound, got.Status)
		assert.Equal(t, RuleUnqualifiedImport, got.Rule)
		assert.Equal(t, "unqualified-import", string(got.Rule))
		require.Len(t, got.Candidates, 1)
		assert.Equal(t, "lib/alpha.ts", got.Candidates[0].File,
			"the constructor binds to the IMPORTED file's class, not the referencing file")

		// KNOWN-POSITIVE CONTROL, in the landed shape and asserting the FULL
		// triple the unbound path produces: with Binds emptied the bare `Alpha`
		// skips the sibling rung (the referencing declaration has no parent),
		// finds nothing of that name in app/main.ts's own scope, and terminates
		// undeclared. Without it this case would pass just as well against a
		// build where the import arm never ran and the reference bound for some
		// unrelated reason.
		unbound := *e.Ref
		unbound.Binds = map[string]treesitter.Bind{}
		ctrl := resolveRef(ix, &unbound, e.ToID)
		assert.NotEqual(t, RuleUnqualifiedImport, ctrl.Rule,
			"with no bind recorded the import rung cannot be what fired")
		assert.Equal(t, RefExternal, ctrl.Status)
		assert.Equal(t, RuleNotDeclared, ctrl.Rule)
		assert.Empty(t, ctrl.Candidates)
	})

	// The remaining rungs need no registry and are driven directly, so each
	// case states exactly the index and site its rule reads.
	ladder := []struct {
		name       string
		index      []*declRec
		ref        *treesitter.RefSite
		target     string
		wantStatus RefStatus
		wantRule   RefRule
		wantValue  string
		wantTarget string // node ID of the single expected candidate, "" when none
	}{
		{
			name: "R2_qualified_parent",
			index: []*declRec{
				{NodeID: "svc/t.go:Thing.Do", File: "svc/t.go", Scope: "dir:svc", Parent: "Thing", Name: "Do"},
			},
			ref:        refSiteFor("svc/c.go", "dir:svc", ""),
			target:     "Thing.Do",
			wantStatus: RefBound,
			wantRule:   RuleQualifiedParent,
			wantValue:  "qualified-parent",
			wantTarget: "svc/t.go:Thing.Do",
		},
		{
			name: "R3_dynamic_scope",
			index: []*declRec{
				// In scope, under two different parents: both are dispatch
				// candidates because a value's type is not known statically.
				{NodeID: "svc/a.go:Alpha.Do", File: "svc/a.go", Scope: "dir:svc", Parent: "Alpha", Name: "Do"},
				{NodeID: "svc/b.go:Beta.Do", File: "svc/b.go", Scope: "dir:svc", Parent: "Beta", Name: "Do"},
				// OUT of scope: the catcher for a ladder that searches by name
				// rather than within one scope unit.
				{NodeID: "other/c.go:Gamma.Do", File: "other/c.go", Scope: "dir:other", Parent: "Gamma", Name: "Do"},
			},
			ref:        refSiteFor("svc/c.go", "dir:svc", ""),
			target:     "value.Do",
			wantStatus: RefDynamic,
			wantRule:   RuleDynamicScope,
			wantValue:  "dynamic-scope",
		},
		{
			// THE RUNG IS EXPRESSED IN A LANGUAGE THAT KEEPS IT, and that moved.
			// This case used a .ts path while refSiteFor hardcodes Go, so it was
			// really a Go site all along — and Go now skips this rung, which
			// would have turned a ladder case into a statement about the gate.
			// Ruby was executed and KEEPS the rung (a bare sibling call runs on
			// the implicit self), so it states R5 without also asserting the
			// gating decision.
			name: "R5_sibling_member",
			index: []*declRec{
				{NodeID: "web/a.rb:Alpha.render", File: "web/a.rb", Scope: "file:web/a.rb", Parent: "Alpha", Name: "render"},
				{NodeID: "web/a.rb:Beta.render", File: "web/a.rb", Scope: "file:web/a.rb", Parent: "Beta", Name: "render"},
			},
			ref:        refSiteForLang("web/a.rb", "file:web/a.rb", "Alpha", treesitter.LangRuby),
			target:     "render",
			wantStatus: RefBound,
			wantRule:   RuleSiblingMember,
			wantValue:  "sibling-member",
			wantTarget: "web/a.rb:Alpha.render",
		},
		{
			name: "R6_own_scope",
			index: []*declRec{
				{NodeID: "svc/a.go:Free", File: "svc/a.go", Scope: "dir:svc", Name: "Free"},
			},
			ref:        refSiteFor("svc/c.go", "dir:svc", ""),
			target:     "Free",
			wantStatus: RefBound,
			wantRule:   RuleOwnScope,
			wantValue:  "own-scope",
			wantTarget: "svc/a.go:Free",
		},
		{
			name: "R7_not_declared",
			index: []*declRec{
				// Declared, but in a scope this reference cannot see.
				{NodeID: "other/a.go:Missing", File: "other/a.go", Scope: "dir:other", Name: "Missing"},
			},
			ref:        refSiteFor("svc/c.go", "dir:svc", ""),
			target:     "Missing",
			wantStatus: RefExternal,
			wantRule:   RuleNotDeclared,
			wantValue:  "not-declared",
		},
	}

	for _, tc := range ladder {
		t.Run(tc.name, func(t *testing.T) {
			ix := indexOf(t, tc.index...)
			got := resolveRef(ix, tc.ref, tc.target)

			assert.Equal(t, tc.wantStatus, got.Status)
			assert.Equal(t, tc.wantRule, got.Rule)
			assert.Equal(t, tc.wantValue, string(got.Rule),
				"the rule's recorded VALUE is what an audit reads")
			require.NotEmpty(t, string(got.Rule), "every return path names the rule that produced it")

			switch {
			case tc.wantTarget != "":
				require.Len(t, got.Candidates, 1)
				assert.Equal(t, tc.wantTarget, got.Candidates[0].NodeID)
			case tc.wantStatus == RefDynamic:
				// Every candidate came from the reference's OWN scope: the
				// out-of-scope declaration must be absent.
				require.Len(t, got.Candidates, 2, "both same-scope declarations are dispatch candidates")
				for _, c := range got.Candidates {
					assert.Equal(t, "dir:svc", c.Scope,
						"a dynamic candidate set is bounded by ONE scope, never searched by name")
				}
			default:
				assert.Empty(t, got.Candidates)
			}
		})
	}

	// THE REFSITE-SHAPE CATCHER. Everything above hands resolveRef a site built
	// by hand, so none of it constrains how the CHUNKER builds one. This case
	// drives the real chunker over a file holding TWO parented declarations
	// with DIFFERENT parents, and requires each sibling reference to bind
	// inside its OWN container.
	//
	// An implementation carrying one RefSite per FILE has a single Parent value
	// and cannot satisfy both directions at once: it either never fires the
	// sibling rule or fires it with the wrong parent. A single-parent fixture
	// would pass under that broken shape.
	//
	// THE FIXTURE IS RUBY, AND IT MOVED FROM TYPESCRIPT. The catcher needs the
	// sibling rung to FIRE in order to observe which parent it fired with, and
	// TypeScript now skips that rung — so in TypeScript the case would assert
	// nothing about RefSite shape and would instead re-assert the gate. Ruby was
	// executed and keeps the rung, so the catcher survives intact rather than
	// being deleted for being inconvenient. Nothing about it is TypeScript-
	// specific: its subject is how many reference sites a file carries.
	t.Run("sibling_member_uses_the_referencing_declarations_own_parent", func(t *testing.T) {
		const path = "web/panels.rb"
		res := populateFixture(t, []fixtureFile{{path: path, src: "" +
			"class Alpha\n" +
			"  def entry\n" +
			"    render()\n" +
			"  end\n\n" +
			"  def render\n" +
			"    'the alpha panel body'\n" +
			"  end\n" +
			"end\n\n" +
			"class Beta\n" +
			"  def entry\n" +
			"    render()\n" +
			"  end\n\n" +
			"  def render\n" +
			"    'a different beta panel body'\n" +
			"  end\n" +
			"end\n"}})

		calls := map[string][]string{}
		for _, e := range res.Edges {
			if kgtypes.EdgeType(e.Type) == kgtypes.EdgeCalls {
				calls[e.FromId] = append(calls[e.FromId], e.ToId)
			}
		}
		require.Equal(t, []string{path + ":Alpha.render"}, calls[path+":Alpha.entry"],
			"a sibling reference inside Alpha binds to Alpha's member, never Beta's")
		require.Equal(t, []string{path + ":Beta.render"}, calls[path+":Beta.entry"],
			"and the other direction must hold in the SAME file — one site per file cannot do both")
	})

	// THE PER-LANGUAGE ARM CASES. Every registered BindsResolver owes a
	// RESOLUTION-level proof — a reference that binds THROUGH the import and
	// would bind elsewhere, or nowhere, without it — because an arm whose
	// carrier delivers nothing returns an empty map, which is indistinguishable
	// from no registration at all and leaves every gate green.
	//
	// They are invoked from here rather than written inline so their subtest
	// names stay this test's, while their bodies live in files that keep this
	// one under the 500-line block.
	ladderImportArmCases(t)
	ladderNamespaceArmCases(t)
}
