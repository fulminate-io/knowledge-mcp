// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// capabilityRow is one language's per-arm capability, with the reason its arms
// exist or do not.
type capabilityRow struct {
	// QualifierArm reports a registered qualifier-types arm.
	QualifierArm bool

	// TypeFactsArm reports a registered TYPE-FACTS arm, FOR ANY PURPOSE.
	//
	// THE NAME IS THE DEFINITION, DELIBERATELY. An arm may serve declared
	// conformance capture, a language's slot binds, or a method-set derivation,
	// and this column does not distinguish them — so it is named for what it
	// measures rather than for one of the things it might mean. A column called
	// after a single purpose would be a false claim the first time a language
	// registered an arm for a different one, and the failure mode of an
	// overstated name is a reader who never reaches the doc that corrects it.
	TypeFactsArm bool

	// Reason states WHAT THIS ROW'S ARMS SERVE, or what the language does not
	// write. IT IS REQUIRED ON EVERY ROW, WITHOUT EXCEPTION — armed and unarmed
	// alike — which is a uniform rule with no classes, no marking field and no
	// judgment call, and is therefore gateable. every_row_states_what_its_arms
	// _serve is the gate, and it is live from the moment this table exists
	// rather than dormant until some later row lands.
	Reason string
}

// testTypedQualifierCensus is the per-language capability table.
//
// WHAT IT DOES NOT ANSWER, stated here because the booleans invite the
// inference: this table says NOTHING about which languages produce
// declared-conformance edges. A type-facts arm may serve conformance capture, a
// slot-bind derivation, or method-set matching — and even an arm that DOES
// capture conformance emits zero edges where the grammar has no contract
// construct at all. "Which languages produce declared-conformance edges" is a
// SEPARATE COLUMN and has to be raised as one. The uniform Reason is what keeps
// that distinction readable from the table even though it is not computable
// from the booleans.
var testTypedQualifierCensus = map[Language]capabilityRow{
	LangGo: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the Go arm serves method-set derivation and captures no declared conformance"},

	LangRust: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the rust arms serve declared conformance; an impl block's trait slot is unambiguous, so its supertype carries the trait kind rather than undeclared"},
	LangSwift: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the swift arms serve declared conformance; its inheritance specifiers are identical for a superclass and a protocol, so every supertype is recorded undeclared"},
	LangCPP: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the cpp arms serve declared conformance from the base-class clause, with a pure-virtual member as the only structural contract signal the language offers"},
	// C IS THE FIRST ROW WHOSE TYPE-FACTS ARM SERVES SOMETHING OTHER THAN
	// CONFORMANCE, which is exactly what the column's name-for-what-it-measures
	// rule was written to allow. Nothing else enforces this Reason: the
	// behavioral subtests below require one only on an unarmed row.
	LangC: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the C type-facts arm serves slot-binds rather than conformance; the language has no supertype construct at all, and a composite literal filling a function-pointer field is its IMPLEMENTS analog"},

	LangTypeScript: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the qualifier arm binds this-receivers, annotated parameters and locals, and constructor " +
			"locals; the type-facts arm serves BOTH purposes — Fields and Results for the typed-qualifier " +
			"rung, and declared-conformance capture from implements, class-extends and interface-extends clauses"},
	LangTSX: {QualifierArm: true, TypeFactsArm: true,
		Reason: "it shares the TypeScript query set and both arms, through its own symbol table because tsx " +
			"is a separate grammar numbering the same kind names differently"},
	LangJavaScript: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the qualifier arm binds this-receivers and constructor locals only, because the language " +
			"has no annotation syntax and JSDoc is not parsed; the type-facts arm exists ONLY to capture " +
			"class heritage, and that capture can emit NO edge at all because the grammar declares no " +
			"contract construct, so every javascript capture resolves to a non-contract or to nothing"},
	LangPython: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the qualifier arm binds self and cls receivers, both annotated parameter forms, annotated " +
			"assignments and constructor calls; the type-facts arm serves annotation-derived Fields and " +
			"Results plus nominal-base conformance capture and the ABC/Protocol contract predicate"},
	LangJava: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the java arms capture declared conformance (extends, implements, and interface extends) and typed qualifiers"},
	LangKotlin: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the kotlin arms capture declared conformance (three delegation-specifier shapes) and typed qualifiers"},
	LangScala: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the scala arms capture declared conformance (extends plus with-mixins) and typed qualifiers"},
	LangCSharp: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the csharp arms capture declared conformance (base lists, every entry kind-undeclared) and typed qualifiers"},
	LangPHP: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the php arms capture declared conformance (extends, implements, and in-body trait use) and typed qualifiers"},
	LangRuby: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the qualifier arm binds the self receiver and the X.new allocator, which is all the " +
			"language offers because ruby writes no annotations and declares no return types; the " +
			"type-facts arm captures superclass and module-mixin conformance and marks a module as the " +
			"contract construct"},
	LangElixir: {TypeFactsArm: true,
		Reason: "the type-facts arm captures @behaviour conformance and marks a module declaring @callback " +
			"as a contract; there is NO qualifier arm because elixir has no receiver-dispatch call form — a " +
			"call is Module.function or bare, a variable never receives a method call, and map.field is data " +
			"access — so a typed-qualifier rung would have nothing to bind"},
	LangGroovy: {QualifierArm: true, TypeFactsArm: true,
		Reason: "the groovy arms capture single-supertype extends conformance (the vendored grammar parses no other clause shape) and typed qualifiers"},
	// THE CLOSING FACT OF THE CENSUS. These two rows are the deliverable, not a
	// leftover: they record that lua and bash were examined and found to declare
	// nothing an arm could read, which is a different claim from nobody having
	// got to them yet. Their reason text is deliberately written on ONE UNBROKEN
	// LINE EACH — a reflowed version would not be greppable, and these are the
	// two rows whose exact bytes are checked.
	LangLua:        {Reason: "no type annotation syntax and no conformance syntax; a table-based prototype assignment is a runtime value, not a declaration"},
	LangBash:       {Reason: "no type syntax and no conformance syntax; langProfiles registers an EXPLICITLY EMPTY separator set (lang_profile.go:119-120) so bash never enters resolveQualified at all"},
	LangElm:        {Reason: "no arm is registered; the language has no subtype relationship to declare"},
	LangOCaml:      {Reason: "no arm is registered; its module and class systems declare no supertype this collector reads"},
	LangHCL:        {Reason: "no arm is registered; it is a configuration language and declares no types"},
	LangProtobuf:   {Reason: "no arm is registered; it is a schema language and declares no supertype"},
	LangSQL:        {Reason: "no arm is registered; it is a query language and declares no supertype"},
	LangCSS:        {Reason: "no arm is registered; it is a stylesheet language and declares no types"},
	LangHTML:       {Reason: "no arm is registered; it is a markup language and declares no types"},
	LangSvelte:     {Reason: "no arm is registered; its component files are markup and declare no supertype"},
	LangDockerfile: {Reason: "no arm is registered; it is a build recipe and declares no types"},
	LangToml:       {Reason: "no arm is registered; it is a data format and declares no types"},
	LangYaml:       {Reason: "no arm is registered; it is a data format and declares no types"},
	LangMarkdown:   {Reason: "no arm is registered; it is a prose format and declares no types"},
	LangCue:        {Reason: "no arm is registered; it is a configuration language and declares no supertype this collector reads"},
}

// censusArmFixture is a source file whose declarations an ARMED language's arms
// must actually populate.
//
// An armed row needs one, because the property "the arm returns non-nil" cannot
// be probed with a nil node: every arm returns nil for a nil node, so a
// nil-input probe reports armed and unarmed languages identically. Reading the
// arm's real output through the production chunk path is the only probe that
// discriminates.
type censusArmFixture struct {
	path string
	src  string
	// declSuffix is the declaration whose reference site must carry qualifier
	// types, read only when QualifierArm is true.
	declSuffix string
}

// EACH FIXTURE MAKES A CALL, because the qualifier half of the probe reads the
// map off a reference EDGE: a declaration that emits no reference carries no
// site for the map to reach, and the probe would report an armed language as
// unarmed. The swift path carries a `Sources/<Module>/` segment because swift's
// resolution unit is derived from that layout, so a fixture written elsewhere
// exercises a different scope than production does.
var censusArmFixtures = map[Language]censusArmFixture{
	LangGo: {
		path:       "pkg/census.go",
		src:        "package p\n\ntype Sink interface {\n\tWrite(s string) error\n}\n\nfunc use() {\n\tc := Ctl{}\n\tc.Do()\n}\n",
		declSuffix: "use",
	},
	LangJava: {
		path: "app/Census.java",
		src: "interface Sink {\n  void write(String r);\n}\n\n" +
			"class Census implements Sink {\n  Store s;\n  public void write(String r) {}\n" +
			"  void use(Store q) { q.go(); }\n}\n",
		declSuffix: "Census.use",
	},
	LangKotlin: {
		path: "app/Census.kt",
		src: "interface Sink {\n  fun write(r: String): Unit\n}\n\n" +
			"class Census : Sink {\n  fun write(r: String): Unit {}\n" +
			"  fun use(q: Store): Unit { q.go() }\n}\n",
		declSuffix: "Census.use",
	},
	LangScala: {
		path: "app/Census.scala",
		src: "trait Sink {\n  def write(r: String): Unit\n}\n\n" +
			"class Census extends Sink {\n  def write(r: String): Unit = {}\n" +
			"  def use(q: Store): Unit = { q.go() }\n}\n",
		declSuffix: "Census.use",
	},
	LangCSharp: {
		path: "app/Census.cs",
		src: "interface ISink {\n  void Write(string r);\n}\n\n" +
			"class Census : ISink {\n  Store s;\n  public void Write(string r) {}\n" +
			"  void Use(Store q) { q.Go(); }\n}\n",
		declSuffix: "Census.Use",
	},
	LangPHP: {
		path: "app/Census.php",
		src: "<?php\ninterface Sink {\n  public function write($r);\n}\n\n" +
			"class Census implements Sink {\n  private Store $s;\n  public function write($r) {}\n" +
			"  function use_it(Store $q) { $q->go(); }\n}\n",
		declSuffix: "Census.use_it",
	},
	LangGroovy: {
		path: "app/Census.groovy",
		src: "interface Sink {\n  void write(String r)\n}\n\n" +
			"interface Loud extends Sink {\n  void shout()\n}\n\n" +
			"class Census {\n  Store s\n  void use(Store q) { q.go() }\n}\n",
		declSuffix: "Census.use",
	},
	LangRust: {
		path: "src/census.rs",
		src: `pub trait Sink {
    fn write(&self, l: Line) -> Ack;
}

pub struct Ctl;

impl Sink for Ctl {
    fn write(&self, l: Line) -> Ack {
        l.take()
    }
}
`,
		declSuffix: "Ctl.write",
	},
	LangSwift: {
		path: "Sources/Census/census.swift",
		src: `protocol Sink {
    func write(l: Line) -> Ack
}

class Ctl: Sink {
    func write(l: Line) -> Ack {
        return l.take()
    }
}
`,
		declSuffix: "Ctl.write",
	},
	LangCPP: {
		path: "src/census.cc",
		src: `class Sink {
 public:
  virtual void write(Line l) = 0;
};

class Ctl : public Sink {
 public:
  void write(Line l) override;
};

void drive(Ctl* c, Line l) {
  c->write(l);
}
`,
		declSuffix: "drive",
	},
	LangC: {
		path: "src/census.c",
		src: `struct http_ops {
  int (*flush)(struct http_conn *h);
};

void drive(struct http_ops *ops, struct http_conn *h) {
  use(ops, h);
}
`,
		declSuffix: "drive",
	},

	// EACH FIXTURE EXERCISES BOTH OF ITS ROW'S ARMS, because the census probes
	// them separately: the qualifier arm through the declaration named by
	// declSuffix, and the type-facts arm through any chunk of the file. A fixture
	// that only bound a qualifier would leave its TypeFactsArm claim unproven
	// while the row still read as armed.
	LangTypeScript: {
		path:       "web/census.ts",
		src:        "interface Sink {\n  write(): void;\n}\n\nclass Svc implements Sink {\n  run(c: Config): void {\n    c.load();\n  }\n}\n",
		declSuffix: "Svc.run",
	},
	LangTSX: {
		path:       "web/census.tsx",
		src:        "interface Sink {\n  write(): void;\n}\n\nclass Svc implements Sink {\n  run(c: Config): void {\n    c.load();\n  }\n}\n",
		declSuffix: "Svc.run",
	},
	LangJavaScript: {
		// javascript's type-facts arm carries class heritage and nothing else, so
		// the fixture must declare a heritage clause or the arm returns nil for
		// every chunk in it.
		path:       "tools/census.mjs",
		src:        "class Svc extends Base {\n  run() {\n    const c = new Client();\n    c.send();\n  }\n}\n",
		declSuffix: "Svc.run",
	},
	LangPython: {
		path:       "bin/census.py",
		src:        "class Sink(ABC):\n    pass\n\n\nclass Svc(Sink):\n    def run(self, cfg: Config):\n        cfg.load()\n",
		declSuffix: "Svc.run",
	},
	LangRuby: {
		path:       "app/census.rb",
		src:        "module Runner\n  def run\n  end\nend\n\nclass Svc < Base\n  include Runner\n  def handle\n    self.log\n  end\nend\n",
		declSuffix: "Svc.handle",
	},
	LangElixir: {
		// ELIXIR IS THE ONE ARMED ROW WITH NO QUALIFIER ARM, so declSuffix is
		// deliberately empty: the census reads it only when QualifierArm is true.
		path: "lib/census.ex",
		src:  "defmodule Worker do\n  @behaviour Runner\n\n  def run(x) do\n    :ok\n  end\nend\n",
	},
}

// TestTypedQualifierCapabilityCensus holds the per-language capability table to
// the registries it describes.
func TestTypedQualifierCapabilityCensus(t *testing.T) {
	t.Run("every_registered_language_has_a_row", func(t *testing.T) {
		// THE SUBJECT LIST IS DERIVED FROM THE REGISTRY, never written out as a
		// literal: a table that enumerated its own subjects would shrink its own
		// proof the day a language is added, while this one fails until the new
		// language is accounted for.
		registered := RegisteredLanguages()
		require.NotEmpty(t, registered, "control: the registry is non-empty, or every assertion here is vacuous")
		for _, lang := range registered {
			require.Containsf(t, testTypedQualifierCensus, lang,
				"%s is a registered language with no census row: add one stating what its arms serve", lang)
		}
		for lang := range testTypedQualifierCensus {
			require.Containsf(t, registered, lang,
				"%s has a census row but is not a registered language", lang)
		}
	})

	t.Run("armed_languages_return_non_nil", func(t *testing.T) {
		// THE KNOWN-POSITIVE CONTROL for the whole table. Without it, a row
		// wrongly marked unarmed would be indistinguishable from a correct one,
		// and the absence subtest below would pass over an empty registry.
		armed := 0
		for lang, row := range testTypedQualifierCensus {
			if !row.QualifierArm && !row.TypeFactsArm {
				continue
			}
			armed++
			fx, ok := censusArmFixtures[lang]
			require.Truef(t, ok,
				"%s is marked armed but supplies no fixture: an arm's output cannot be probed with a nil node, because every arm returns nil for one", lang)

			c := NewChunker()
			t.Cleanup(c.Close)
			res, err := c.ChunkFile(context.Background(), fx.path, []byte(fx.src))
			require.NoError(t, err)
			require.NotEmptyf(t, res.Chunks, "%s fixture control: the file produced chunks at all", lang)

			if row.QualifierArm {
				require.NotNilf(t, qualTypesFor(t, res, fx.declSuffix),
					"%s is marked QualifierArm but its reference site carries no qualifier types", lang)
			}
			if row.TypeFactsArm {
				var sawFacts bool
				for _, ch := range res.Chunks {
					if ch.TypeFacts != nil {
						sawFacts = true
						break
					}
				}
				require.Truef(t, sawFacts,
					"%s is marked TypeFactsArm but no chunk of its fixture carries type facts", lang)
			}
		}
		require.Positive(t, armed,
			"no row is armed, so this subtest asserted nothing: the table has lost the control every absence below rests on")
	})

	t.Run("unarmed_languages_return_nil", func(t *testing.T) {
		// THE REGISTRY IS THE SUBJECT, not the dispatcher's answer for a nil
		// node — that answer is nil for armed and unarmed languages alike, so a
		// nil-input probe alone would pass whatever the registries held.
		for lang, row := range testTypedQualifierCensus {
			if !row.QualifierArm {
				require.NotContainsf(t, qualifierTypeResolvers, lang,
					"%s is marked unarmed for qualifier types but an arm is registered for it", lang)
				require.Nilf(t, qualifierTypesFor(lang, nil, nil),
					"%s must reach no qualifier-types arm", lang)
			}
			if !row.TypeFactsArm {
				require.NotContainsf(t, typeFactsResolvers, lang,
					"%s is marked unarmed for type facts but an arm is registered for it", lang)
				require.Nilf(t, typeFactsFor(lang, nil, "type_declaration", nil),
					"%s must reach no type-facts arm", lang)
			}
		}
		// KNOWN-POSITIVE CONTROL: the registries are not simply empty, so an
		// absence above is language dispatch rather than nothing registered.
		require.NotEmpty(t, qualifierTypeResolvers, "control: at least one qualifier-types arm is registered")
		require.NotEmpty(t, typeFactsResolvers, "control: at least one type-facts arm is registered")
	})

	t.Run("every_row_states_what_its_arms_serve", func(t *testing.T) {
		// LIVE FROM THE MOMENT THE TABLE EXISTS rather than dormant until some
		// later row lands: the armed row must carry its reason too, so this
		// fails today against a table that leaves any Reason blank.
		registered := RegisteredLanguages()
		require.NotEmpty(t, registered, "control: the registry is non-empty, so the loop below cannot pass by reading nothing")
		for _, lang := range registered {
			row := testTypedQualifierCensus[lang]
			require.NotEmptyf(t, row.Reason,
				"%s: every row states what its arms serve, or what the language does not write — armed rows included", lang)
		}
	})
}
