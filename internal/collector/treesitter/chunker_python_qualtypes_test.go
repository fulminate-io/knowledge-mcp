// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPythonQualifierTypes covers the python arm's four binding routes and the
// three annotation shapes that decline.
func TestPythonQualifierTypes(t *testing.T) {
	t.Run("self_receiver", func(t *testing.T) {
		const src = `class Server:
    def run(self, cfg):
        self.store.get()
        cfg.load()
`
		res := chunkQualFixture(t, "bin/recv.py", src)
		got := qualTypesFor(t, res, "Server.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Server"}, got["self"], "the receiver binds to its class")
	})

	t.Run("cls_receiver", func(t *testing.T) {
		// THE NAME IS TAKEN AS WRITTEN. A classmethod's receiver is `cls`, and an
		// arm matching the literal `self` would bind the first case and miss this
		// one entirely.
		const src = `class Server:
    @classmethod
    def make(cls, cfg):
        cls.build()
        cfg.load()
`
		res := chunkQualFixture(t, "bin/cls.py", src)
		got := qualTypesFor(t, res, "Server.make")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Server"}, got["cls"], "a classmethod's receiver binds under its own name")
	})

	t.Run("staticmethod_first_parameter_is_not_a_receiver", func(t *testing.T) {
		// A STATICMETHOD TAKES NO RECEIVER, so `payload` is an ordinary argument
		// of whatever type the caller passed. Binding it to the enclosing class
		// does not merely miss a binding — it MANUFACTURES A WRONG TARGET, and
		// the end-to-end effect is a CALLS edge from the staticmethod to a method
		// of its own class that the argument may have nothing to do with.
		const src = `class Server:
    @staticmethod
    def helper(payload):
        payload.handle()

    @classmethod
    def make(cls, cfg):
        cls.build()
`
		res := chunkQualFixture(t, "bin/static.py", src)

		// THE KNOWN-POSITIVE IS IN THE SAME FIXTURE, and it is the one decorator
		// that must keep binding: @classmethod still takes a receiver, spelled
		// cls. Without it, an arm that had simply stopped binding receivers
		// altogether would satisfy the absence below.
		madeTypes := qualTypesFor(t, res, "Server.make")
		require.NotEmpty(t, madeTypes, "control: the classmethod bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Server"}, madeTypes["cls"],
			"control: @classmethod still binds its receiver")

		helperTypes := qualTypesFor(t, res, "Server.helper")
		_, boundPayload := helperTypes["payload"]
		assert.False(t, boundPayload,
			"a staticmethod's first parameter is an ordinary argument, not a receiver")
	})

	t.Run("module_level_function_binds_no_receiver", func(t *testing.T) {
		// The known-negative half of the receiver route: with no enclosing class
		// there is nothing to bind the first parameter to, and binding it anyway
		// would attribute a type to any first argument in the codebase.
		const src = `def run(thing, cfg: Config):
    thing.go()
    cfg.load()
`
		res := chunkQualFixture(t, "bin/mod.py", src)
		got := qualTypesFor(t, res, "run")
		require.NotEmpty(t, got, "control: the declaration bound its annotated parameter")
		assert.Equal(t, QualType{Text: "Config"}, got["cfg"], "control: the parameter route still works")

		_, boundFirst := got["thing"]
		assert.False(t, boundFirst, "a module-level function's first parameter is not a receiver")
	})

	t.Run("typed_parameter", func(t *testing.T) {
		const src = `class Server:
    def run(self, cfg: Config, m: mod.Other, s: "Bar", l: list[Foo]):
        cfg.load()
`
		res := chunkQualFixture(t, "bin/param.py", src)
		got := qualTypesFor(t, res, "Server.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")

		assert.Equal(t, QualType{Text: "Config"}, got["cfg"], "a bare annotation binds")
		assert.Equal(t, QualType{Text: "mod.Other"}, got["m"], "a dotted annotation keeps its qualifier")

		// THE DECLINES. A string forward reference is not a name node, and a
		// subscripted container names a type whose methods the value does not have.
		_, boundString := got["s"]
		_, boundSubscript := got["l"]
		assert.False(t, boundString, "a string forward reference declines")
		assert.False(t, boundSubscript, "a subscripted container declines")
	})

	t.Run("typed_default_parameter", func(t *testing.T) {
		// A DISTINCT NODE KIND, not a variant of typed_parameter — so an arm
		// naming only the latter misses every annotated parameter carrying a
		// default, which in real python is most of the optional ones.
		const src = `class Server:
    def run(self, cfg: Config = None, o: Other = None):
        cfg.load()
        o.go()
`
		res := chunkQualFixture(t, "bin/default.py", src)
		got := qualTypesFor(t, res, "Server.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Config"}, got["cfg"], "a defaulted annotated parameter binds")
		assert.Equal(t, QualType{Text: "Other"}, got["o"])
	})

	t.Run("annotated_assignment", func(t *testing.T) {
		const src = `class Server:
    def run(self):
        v: Local = mk()
        v.use()
`
		res := chunkQualFixture(t, "bin/assign.py", src)
		got := qualTypesFor(t, res, "Server.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")
		assert.Equal(t, QualType{Text: "Local"}, got["v"],
			"an annotated local binds its annotation, not its initialiser")
	})

	t.Run("constructor_call", func(t *testing.T) {
		const src = `class Server:
    def run(self):
        c = Client()
        d = mod.Client()
        c.send()
        d.send()
`
		res := chunkQualFixture(t, "bin/ctor.py", src)
		got := qualTypesFor(t, res, "Server.run")
		require.NotEmpty(t, got, "control: the declaration bound qualifiers at all")

		// In python the callee of a construction IS the class, so this is a DIRECT
		// binding rather than a call whose result type must be looked up.
		assert.Equal(t, QualType{Text: "Client"}, got["c"], "a construction binds the class directly")

		_, boundDotted := got["d"]
		assert.False(t, boundDotted, "a dotted callee declines rather than taking a second hop")
	})
}

// TestPythonConformanceCapture covers the base-list capture and the contract
// predicate in both directions.
func TestPythonConformanceCapture(t *testing.T) {
	t.Run("nominal_base", func(t *testing.T) {
		res := chunkQualFixture(t, "bin/base.py",
			"class Server(Plain, mod.Other):\n    pass\n")
		got, ok := conformsOf(t, res, "Server")
		require.True(t, ok, "control: the class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{
			{Text: "Plain", Kind: ConformExtends},
			{Text: "mod.Other", Kind: ConformExtends},
		}, got, "each nominal base is captured, dotted spelling retained")
	})

	t.Run("keyword_argument_declines", func(t *testing.T) {
		// A metaclass= is a construction directive, not a supertype. Recording it
		// as one would state a conformance the source never declared.
		res := chunkQualFixture(t, "bin/kw.py",
			"class Server(Plain, metaclass=Meta):\n    pass\n")
		got, ok := conformsOf(t, res, "Server")
		require.True(t, ok, "control: the class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Plain", Kind: ConformExtends}}, got,
			"the plain base is captured and the keyword argument is not")
		for _, c := range got {
			assert.NotEqual(t, "Meta", c.Text, "the metaclass is not a declared supertype")
		}
	})

	t.Run("bare_and_empty_base_lists", func(t *testing.T) {
		// TWO SHAPES AND ONLY ONE IS OBVIOUS: `class Bare:` carries no
		// argument_list node at all while `class Empty()` carries an empty one, so
		// a walk assuming the node is present would fail on the commonest class in
		// any codebase.
		res := chunkQualFixture(t, "bin/bare.py",
			"class Bare:\n    pass\n\n\nclass Empty():\n    pass\n\n\nclass Real(Plain):\n    pass\n")
		bare, _ := conformsOf(t, res, "Bare")
		empty, _ := conformsOf(t, res, "Empty")
		assert.Empty(t, bare, "a class with no base list declares no supertype")
		assert.Empty(t, empty, "a class with an empty base list declares no supertype either")

		// KNOWN-POSITIVE CONTROL in the same run, so the two emptinesses are the
		// shapes being handled rather than the arm being inert.
		real, ok := conformsOf(t, res, "Real")
		require.True(t, ok, "control: a class WITH a base still captures it")
		assert.Equal(t, []DeclaredSupertype{{Text: "Plain", Kind: ConformExtends}}, real)
	})

	t.Run("protocol_base_is_captured_as_base", func(t *testing.T) {
		// THE HALF A READER WOULD OTHERWISE TAKE FOR AN OVERSIGHT. Naming Protocol
		// does two independent things: it makes THIS class a contract, and it
		// records Protocol itself as an ordinary captured base. Structural
		// satisfaction — a class shaped like a Protocol without naming it —
		// declares nothing and is deliberately not matched.
		res := chunkQualFixture(t, "bin/proto.py",
			"class Sink(Protocol):\n    def write(self):\n        ...\n\n\n"+
				"class Structural:\n    def write(self):\n        pass\n")
		got, ok := conformsOf(t, res, "Sink")
		require.True(t, ok, "control: the protocol class carries type facts at all")
		assert.Equal(t, []DeclaredSupertype{{Text: "Protocol", Kind: ConformExtends}}, got,
			"Protocol is recorded as an ordinary nominal base like any other")

		structural, _ := conformsOf(t, res, "Structural")
		assert.Empty(t, structural,
			"a class that structurally satisfies a protocol without naming it declares nothing")
		assert.False(t, isContract(t, res, "Structural"),
			"structural satisfaction is deliberately NOT matched")
	})

	t.Run("abc_base_marks_contract", func(t *testing.T) {
		res := chunkQualFixture(t, "bin/abc.py",
			"import abc\n\n\nclass A(ABC):\n    pass\n\n\nclass B(abc.ABC):\n    pass\n\n\n"+
				"class C(Protocol):\n    pass\n\n\nclass D(typing.Protocol):\n    pass\n\n\n"+
				"class E(metaclass=abc.ABCMeta):\n    pass\n")
		for _, name := range []string{"A", "B", "C", "D", "E"} {
			assert.Truef(t, isContract(t, res, name),
				"%s names an abstract base or metaclass in its OWN base list, so it is a contract", name)
		}
	})

	t.Run("plain_base_is_not_a_contract", func(t *testing.T) {
		// THE PREDICATE READS THE CLASS'S OWN BASE LIST AND NOTHING ELSE. Sub is
		// the case that matters: it inherits from an in-repo ABC, which makes it a
		// concrete subclass of a contract rather than a contract — marking it one
		// would fan every call resolved to it across ITS subclasses.
		res := chunkQualFixture(t, "bin/plain.py",
			"class Sink(ABC):\n    pass\n\n\nclass Plain:\n    pass\n\n\n"+
				"class Other(Base):\n    pass\n\n\nclass Sub(Sink):\n    pass\n")
		require.True(t, isContract(t, res, "Sink"), "control: the predicate is not simply always false")
		assert.False(t, isContract(t, res, "Plain"), "a class with no bases is not a contract")
		assert.False(t, isContract(t, res, "Other"), "a plain nominal base does not make a contract")
		assert.False(t, isContract(t, res, "Sub"),
			"inheriting from a contract makes a concrete subclass, not another contract")
	})
}
