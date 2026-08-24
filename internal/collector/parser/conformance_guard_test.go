// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// nominalGuardFixture is one armed language's source file, chosen so its
// declarations DO declare a supertype that resolves to an in-repo contract.
//
// The shape is per language rather than shared, because "declares a supertype
// that is a contract" is spelled differently in each grammar, and a fixture
// that merely parsed would prove nothing about capture.
type nominalGuardFixture struct {
	path string
	src  string
}

// nominalGuardFixtures covers every language NominalArmedLanguages names. The
// map is keyed by language and the test requires a fixture for each, so a
// language added to the armed set without a fixture fails loudly rather than
// being skipped.
//
// GROOVY'S FIXTURE IS AN INTERFACE EXTENDING AN INTERFACE, AND THAT IS FORCED
// RATHER THAN CHOSEN. Its grammar declares no `implements` token at all and
// cannot represent a combined or multi-supertype clause without an ERROR node,
// so a single-supertype `extends` is the whole of what it can capture; and
// because a supertype resolving to a non-contract emits nothing, a class
// extending a class would capture its entry and emit no edge. An interface
// extending an interface is therefore the only groovy declaration that produces
// an edge at all.
var nominalGuardFixtures = map[treesitter.Language]nominalGuardFixture{
	treesitter.LangJava: {
		path: "app/Server.java",
		src: "interface Greeter {\n  void greet();\n}\n\n" +
			"class Server implements Greeter {\n  public void greet() {}\n}\n",
	},
	treesitter.LangKotlin: {
		path: "app/Server.kt",
		src: "interface Greeter {\n  fun greet(): Unit\n}\n\n" +
			"class Server : Greeter {\n  fun greet(): Unit {}\n}\n",
	},
	treesitter.LangScala: {
		path: "app/Server.scala",
		src: "trait Greeter {\n  def greet(): Unit\n}\n\n" +
			"class Server extends Greeter {\n  def greet(): Unit = {}\n}\n",
	},
	treesitter.LangCSharp: {
		path: "app/Server.cs",
		src: "interface IGreeter {\n  void Greet();\n}\n\n" +
			"class Server : IGreeter {\n  public void Greet() {}\n}\n",
	},
	treesitter.LangPHP: {
		path: "app/Server.php",
		src: "<?php\ninterface Greeter {\n  public function greet();\n}\n\n" +
			"class Server implements Greeter {\n  public function greet() {}\n}\n",
	},
	treesitter.LangGroovy: {
		path: "app/Server.groovy",
		src: "interface Greeter {\n  void greet()\n}\n\n" +
			"interface Loud extends Greeter {\n  void shout()\n}\n",
	},
}

// goGuardControl is the KNOWN-POSITIVE CONTROL for the method-set half. Without
// it, a populate path that produced no IMPLEMENTS edge for anyone — a broken
// harness, a derivation that never ran — would satisfy the absence assertion
// forever.
var goGuardControl = nominalGuardFixture{
	path: "pkg/svc.go",
	src: "package pkg\n\ntype Greeter interface {\n\tGreet(s string) error\n}\n\n" +
		"type Server struct{}\n\nfunc (s Server) Greet(v string) error { return nil }\n",
}

// TestNominalLanguagesGetNoMethodSetPairs holds the six nominal-static
// languages to the two outcomes this group requires of the derivation path.
//
// IT ASSERTS OUTCOMES AND NEVER A GATE SHAPE. There is no grep for a language
// check, a map or a call site: how the method-set derivation is scoped belongs
// to the shared mechanism, and a test that pinned the mechanism's shape would
// have to be rewritten by any correct change to it.
//
// THE TWO HALVES HAVE DIFFERENT STANDING, AND MISLABELLING THEM WOULD BE THE
// EASY LIE. The method-set half is a CHARACTERIZATION GUARD over a gate that
// already exists — it was green the day it was written and is here to stay
// green. The declared-conformance half is the RED-FIRST half: against a tree
// carrying no capture arms it fails, and the failure to expect is a ZERO
// conformance-edge count for an armed language — never a nil dereference and
// never a build error, both of which mean the harness is broken rather than the
// arms missing.
func TestNominalLanguagesGetNoMethodSetPairs(t *testing.T) {
	armed := treesitter.NominalArmedLanguages()
	require.NotEmpty(t, armed,
		"control: the armed-language set is non-empty, or every loop below asserts nothing")

	t.Run("no_method_set_pairs_for_nominal_languages", func(t *testing.T) {
		for _, lang := range armed {
			fx, ok := nominalGuardFixtures[lang]
			require.Truef(t, ok, "%s is armed but carries no guard fixture", lang)

			res := populateFixture(t, []fixtureFile{{path: fx.path, src: fx.src}})
			require.NotEmptyf(t, res.Nodes, "%s fixture control: the file produced nodes at all", lang)

			for _, e := range guardImplementsEdges(res) {
				require.Falsef(t, strings.HasPrefix(e.Method, kgtypes.EdgeMethodMethodSet),
					"%s: %s -> %s carries %q. The method-set derivation infers satisfaction from "+
						"signature comparison, and these languages DECLARE their conformance — an "+
						"inferred pair here is unfounded.", lang, e.FromId, e.ToId, e.Method)
			}
		}
	})

	t.Run("go_still_derives_method_set_pairs", func(t *testing.T) {
		res := populateFixture(t, []fixtureFile{{path: goGuardControl.path, src: goGuardControl.src}})
		var sawMethodSet bool
		for _, e := range guardImplementsEdges(res) {
			if strings.HasPrefix(e.Method, kgtypes.EdgeMethodMethodSet) {
				sawMethodSet = true
			}
		}
		require.True(t, sawMethodSet,
			"control: Go DOES derive method-set pairs, so the absence asserted above is language "+
				"scoping rather than a derivation that produced nothing for anybody")
	})

	t.Run("each_armed_language_produces_a_declared_conformance_edge", func(t *testing.T) {
		for _, lang := range armed {
			fx, ok := nominalGuardFixtures[lang]
			require.Truef(t, ok, "%s is armed but carries no guard fixture", lang)

			res := populateFixture(t, []fixtureFile{{path: fx.path, src: fx.src}})
			require.NotEmptyf(t, res.Nodes, "%s fixture control: the file produced nodes at all", lang)

			var declared int
			for _, e := range guardImplementsEdges(res) {
				if strings.HasPrefix(e.Method, kgtypes.EdgeMethodDeclaredConformance) {
					declared++
				}
			}
			require.Positivef(t, declared,
				"%s is armed but its fixture produced NO declared-conformance edge. This is the "+
					"expected failure against a tree whose capture arms are not yet registered; a "+
					"panic or a build error instead means the harness is wrong, not the arms.", lang)
		}
	})
}

// guardImplementsEdges returns every IMPLEMENTS edge in a populate result.
//
// It is separate from the sibling implementsEdgesOf helper, which REQUIRES a
// non-empty result: two of the three subtests here are about a population that
// may legitimately be empty, and an emptiness control belongs on the subtest
// that knows which emptiness is correct rather than on the accessor.
func guardImplementsEdges(res PopulateResult) []*knowledgev1.Edge {
	var out []*knowledgev1.Edge
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeImplements {
			out = append(out, e)
		}
	}
	return out
}
