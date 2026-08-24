// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// QualifierTypeResolver walks ONE declaration's subtree and returns the types
// its locally-visible qualifier names were declared with — the receiver, the
// parameters, the locals — keyed by the name a reference inside that
// declaration would use.
//
// It returns nil when the declaration establishes nothing, and nil is a
// meaningful answer rather than an empty one: refForDeclaration delegates a nil
// map to refForParent verbatim, so a declaration that binds nothing carries the
// exact reference site it carried before this rung existed.
type QualifierTypeResolver func(declNode *sitter.Node, src []byte) map[string]QualType

// qualifierTypeResolvers holds the registered per-language arms. It is the
// fourth per-language registry in this package, after declNameResolvers,
// testKindClassifiers and bindsResolvers, and it follows their shape: empty
// until a language's own init installs an arm, and inert for every language
// that ships none.
var qualifierTypeResolvers = map[Language]QualifierTypeResolver{}

// RegisterQualifierTypes installs the qualifier-type arm for one language.
//
// IT OVERWRITES SILENTLY RATHER THAN PANICKING ON A DUPLICATE, deliberately
// deviating from the init-time-registry practice card's panic-on-duplicate
// rule. This registry follows the in-tree RegisterBindsResolver idiom instead,
// because tests must be able to swap an arm in and restore the production one —
// which is the card's own documented "when NOT to use" case. The hazard worth
// avoiding here is the one RegisterBindsResolver documents: an unregistered
// production arm silently disarming every later test in the same binary.
func RegisterQualifierTypes(lang Language, r QualifierTypeResolver) {
	qualifierTypeResolvers[lang] = r
}

// UnregisterQualifierTypes removes a language's arm, restoring the
// unregistered state exactly.
//
// It is exported for the reason UnregisterBindsResolver is: an arm-off parity
// run must be able to take the arm out before any chunking, because a baseline
// that leaves the arm registered compares a number against itself. The same
// inverse hazard applies — a test that unregisters a PRODUCTION arm disarms the
// feature for every later test in the same binary, so a test that installs a
// fake over the production arm must restore the production registration in its
// cleanup rather than merely deleting its fake.
func UnregisterQualifierTypes(lang Language) {
	delete(qualifierTypeResolvers, lang)
}

// QualifierTypesArm returns the arm registered for one language, and whether
// there is one.
//
// It exists so a MEASUREMENT harness can wrap a production arm rather than
// replace it — counting what the real arm offers, then delegating to it — which
// is the only way to observe the arm's supply side without either duplicating
// the walk or instrumenting the production path itself. Reading the registry is
// the third operation on it, beside registering and unregistering, and it is
// exported for the same reason those two are.
func QualifierTypesArm(lang Language) (QualifierTypeResolver, bool) {
	r, ok := qualifierTypeResolvers[lang]
	return r, ok
}

// qualifierTypesFor runs the registered arm for one declaration's language, or
// returns nil when none is registered.
//
// NIL IS THE WHOLE OF THE UNREGISTERED CONTRACT. Every language without an arm
// reaches refForDeclaration's nil branch, which delegates to refForParent — so
// those languages keep byte-identical reference sites and pay one nil map read
// per declaration rather than an allocation.
func qualifierTypesFor(lang Language, declNode *sitter.Node, src []byte) map[string]QualType {
	r, ok := qualifierTypeResolvers[lang]
	if !ok {
		return nil
	}
	return r(declNode, src)
}

// ConformanceKind names the CLAUSE a declaration used to declare a supertype.
//
// IT IS WHAT A CONSUMER OF THE EDGE READS to tell a ruby module include from a
// TypeScript implements — the resolved edge is the same shape in both cases, so
// without the kind the two are indistinguishable downstream and a consumer that
// wants to treat them differently has nothing to key on.
//
// The vocabulary is CLOSED at six members, and closing it is the point: a
// seventh kind is a deliberate vocabulary decision made HERE, where every
// consumer reads it, never a local invention in one language's arm file.
//
// ConformUndeclared IS A FIRST-CLASS ANSWER, NOT A FAILURE. Two grammars force
// it, and both were verified by parsing rather than assumed:
//
//   - C# — a base_list's children are plain identifier/qualified_name nodes
//     carrying NO class-versus-interface marker, so the clause cannot say which
//     it named.
//   - KOTLIN — a delegation_specifier is a BARE user_type whenever the supertype
//     takes no constructor invocation. `class Server : Base(), Greeter, Logger`
//     yields a first specifier containing a constructor_invocation (which does
//     prove a class) and two bare user_type specifiers; `class Plain : Greeter {}`
//     yields a bare user_type with no constructor_invocation at all — and that
//     same shape is what a CONSTRUCTOR-LESS CLASS supertype produces. So the
//     bare form cannot be attributed.
//
// Swift's inheritance_specifier nodes are structurally identical for a
// superclass and for a protocol, so swift reaches the same answer.
//
// SCALA IS NOT ONE OF THE FORCING GRAMMARS. Parsing
// `class Server extends Base with Greeter with Logger` yields extends_clause
// children `extends` (anonymous), type_identifier, `with` (anonymous),
// type_identifier, `with` (anonymous), type_identifier: the keywords ARE
// anonymous, but they are ORDERED AND WALKABLE, so an arm can give the type
// after `extends` ConformExtends and each type after a `with` ConformMixin. An
// arm that emitted ConformUndeclared for scala would be discarding information
// the tree carries.
//
// An arm that GUESSED a kind where its grammar cannot tell would state a fact
// the tree does not carry. Where the grammar cannot tell, ConformUndeclared is
// the honest answer.
type ConformanceKind string

// The closed conformance-kind vocabulary. Exactly six members.
const (
	ConformImplements ConformanceKind = "implements"
	ConformExtends    ConformanceKind = "extends"
	ConformMixin      ConformanceKind = "mixin"
	ConformBehaviour  ConformanceKind = "behaviour"
	ConformTrait      ConformanceKind = "trait"
	ConformUndeclared ConformanceKind = "undeclared"
)

// DeclaredSupertype is ONE supertype a declaration declared, with the clause it
// was declared under.
type DeclaredSupertype struct {
	// Text is the supertype's spelling AS WRITTEN, under the SAME normalization
	// contract TypeFacts.Embeds documents: type arguments stripped, qualifier and
	// any leading namespace separator RETAINED. Binding a name to a scope is the
	// parser's job and it must happen against the DECLARING file's imports, so
	// the qualifier is the parser's input and stripping it here would destroy it.
	//
	// NO IDENTIFIER NORMALIZATION HAPPENS HERE OR DOWNSTREAM. PHP legitimately
	// needs a qualifier key carrying the "$" sigil beside a member name without
	// one, and nothing may collapse the two.
	Text string

	// Kind is the clause the supertype was declared under, or ConformUndeclared
	// where the grammar cannot say.
	Kind ConformanceKind
}

// SlotBind is ONE slot of a composite literal filled with a named target: a
// struct field bound to the function that implements it.
//
// IT IS C's IMPLEMENTS ANALOG AT THE SYNTAX LEVEL. C declares no supertype, so
// there is no clause to read; what it has instead is a struct of function
// pointers filled by a literal, and the field-to-function pair that literal
// writes is the same relationship a declared conformance states outright.
//
// EXACTLY ONE OF Field AND Index IS SET, AND THAT IS THE SHAPE RATHER THAN A
// CONVENTION. A designated pair names its field, so Field carries the name and
// Index is -1; a positional element names nothing, so Field is empty and Index
// carries its zero-based position for the declaration's own field order to
// resolve.
//
// THESE ARE SPELLINGS, NOT RESOLVED TARGETS, for the reason the whole carrier
// gives: the chunker holds no declaration index, so binding a name to a
// declaration is the parser's job.
type SlotBind struct {
	// Type is the initialized variable's declared type spelling AS WRITTEN,
	// normalized by the same closed allowlist the C qualifier arm binds
	// through, so `struct http_ops` records `http_ops`.
	Type string
	// Field is the designated field's name, or "" for a positional element.
	Field string
	// Target is the assigned identifier's spelling, with a leading `&`
	// stripped: taking the address of a function names the same function.
	Target string
	// Index is a positional element's zero-based position, or -1 for a
	// designated pair.
	Index int
}

// TypeFacts are one declaration's syntax-visible type facts. The chunker holds
// no declaration index, so these are TYPE NAMES rather than resolved targets:
// binding a name to a scope is the parser's job, and it must happen against the
// DECLARING file's imports rather than a referencing file's.
//
// The texts are NORMALIZED by the language arm before they land here — pointer
// stars, parens and type arguments stripped, container types declined — so that
// every text stored is one that resolution could actually match. See the class
// rule on the Go arm's goTypeFacts.
type TypeFacts struct {
	// Results are a function's or method's declared result types, IN ORDER.
	// Order is load-bearing: it is what a multi-value binding's ResultIndex
	// indexes into, so a result whose type this rung cannot bind is recorded as
	// the EMPTY STRING to hold its position rather than dropped.
	Results []string

	// Fields maps a struct's field name to its declared type. A field whose
	// type cannot be bound is OMITTED — this map is keyed by name, so absent
	// and empty mean the same thing.
	Fields map[string]string

	// Embeds are a type declaration's embedded-type SPELLINGS, struct fields and
	// interface elements alike, as written. A spelling that names nothing in-repo
	// is recorded here and declines at resolution — a consumer needs to know an
	// interface has an unexpandable embed, because its method set is then
	// under-known rather than merely small.
	Embeds []string

	// Conforms are the supertypes this declaration DECLARED, with the clause
	// each was declared under.
	//
	// IT IS DELIBERATELY NOT Embeds, AND THE REASON IS MEASURED RATHER THAN
	// STYLISTIC. Embeds feeds the Go method-set derivation's embed expansion, so
	// a conformance spelling routed into Embeds enters that expansion and MOVES
	// the landed derivation's output. The two carriers answer different
	// questions and a declaration may legitimately fill both.
	//
	// NIL MEANS "THIS DECLARATION DECLARES NO SUPERTYPE". That is the common
	// answer in every language, it is the zero value, and it costs one nil slice
	// header rather than an allocation — the same free-nothing contract Fields
	// documents.
	Conforms []DeclaredSupertype

	// SlotBinds are the composite-literal slots this declaration filled with a
	// named target.
	//
	// NIL MEANS "THIS DECLARATION BINDS NO SLOT" — the same nil-is-an-answer
	// contract Conforms and Fields carry, and the common answer in every
	// language, since only C registers an arm that fills it.
	SlotBinds []SlotBind

	// FieldOrder is a STRUCT declaration's field names IN SOURCE ORDER, which
	// is what a POSITIONAL slot bind's Index indexes into.
	//
	// IT LISTS EVERY FIELD, including ones Fields omits because their type
	// could not be bound, and the EMPTY STRING for an anonymous member so
	// positions stay true. That is the Results ordering contract applied to a
	// second carrier, and for the same reason Results gives: a dropped entry
	// shifts every later position and silently rebinds them to the wrong field.
	// Fields is a MAP and structurally cannot carry order, which is why this is
	// a separate carrier rather than a reading of that one.
	FieldOrder []string

	// Sig is the declaration's COMPOSED signature, kept separate from Results
	// because the two answer different questions: Results is what a call's value
	// binds to, Sig is the declaration's identity for comparing one declaration's
	// signature against another's. Nil when the declaration has no signature.
	Sig *SigFacts

	// IsInterface marks a type declaration whose spec declares an interface
	// rather than a concrete type. It is recorded at chunk time because the only
	// place the distinction is cheaply visible is the parse tree; a downstream
	// consumer would otherwise have to re-parse to tell a contract from a struct.
	IsInterface bool

	// PartialBody marks a container that is ONE BODY of a type which may have
	// several, so another declaration sharing its scope and name is the SAME
	// type rather than a sibling that merely collides with it.
	//
	// IT IS RECORDED BECAUSE THE ANSWER IS LANGUAGE SEMANTICS, NOT SHAPE. Two
	// same-named containers in one scope mean opposite things in different
	// languages: C# `partial` blocks are one type, while a scala companion, a
	// family of generic arities and a twice-vendored library are distinct types
	// that happen to collide. Only the language's own arm can tell them apart,
	// so it states the fact here rather than leaving a downstream consumer to
	// infer it from a shape that cannot carry it.
	PartialBody bool

	// IsGeneric marks a type declaration carrying type PARAMETERS.
	//
	// IT IS RECORDED STRUCTURALLY BECAUSE IT CANNOT BE INFERRED DOWNSTREAM. A
	// type parameter is an unqualified identifier, so by the time a signature has
	// been resolved it is indistinguishable from a same-package type — and
	// "resolves to a name the index does not declare" does NOT separate them,
	// because a type ALIAS (`type Edge = other.Edge`) is a real same-package type
	// that the declaration query never captures. Measured against the frozen
	// knowledge corpus, an inference on that rule misclassified store.DB — which
	// has no type parameters at all — as generic, because its methods name the
	// aliased store.Edge. The parse tree is the only place the question has a
	// straight answer.
	IsGeneric bool
}

// TypeExprLeafSep separates a TypeExpr's composition shape from the leaves the
// parser substitutes into it. ASCII unit separator — a Go type spelling is an
// identifier or a dotted identifier and can never contain it.
const TypeExprLeafSep = "\x1f"

// TypeExpr is one type expression split into the part the chunker can decide
// and the part only the parser can.
//
// THE SPLIT IS THE POINT. Composition — pointer, slice, map, channel, variadic,
// function, generic instantiation — is fully visible in the parse tree, so the
// chunker renders it. WHICH DECLARATION a leaf names is not: binding `Foo` to a
// scope depends on the DECLARING file's imports and on what the repo actually
// declares, which is the parser's index. So the shape travels with placeholders
// and the written spellings travel beside it, in left-to-right order, and the
// parser substitutes resolved leaves into the shape.
type TypeExpr struct {
	// Shape is the composition with each resolvable leaf replaced by
	// TypeExprLeafSep.
	Shape string
	// Leaves are those leaves' WRITTEN spellings, left to right. len(Leaves)
	// equals the number of separators in Shape, by construction.
	Leaves []string
}

// SigFacts is a declaration's signature as composed type expressions.
//
// THE RECEIVER IS DELIBERATELY ABSENT. A concrete method's receiver and an
// interface method spec's absent receiver must render identically, or a spec
// could never match the method that satisfies it — which is the entire reason
// this carrier exists.
type SigFacts struct {
	Params  []TypeExpr
	Results []TypeExpr
}

// TypeFactsResolver extracts one declaration's type facts, or returns nil when
// the declaration carries none.
type TypeFactsResolver func(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts

// typeFactsResolvers holds the registered per-language arms, on the same
// closed-registry shape as qualifierTypeResolvers above.
var typeFactsResolvers = map[Language]TypeFactsResolver{}

// RegisterTypeFacts installs the type-facts arm for one language. It overwrites
// silently for the same reason RegisterQualifierTypes does.
func RegisterTypeFacts(lang Language, r TypeFactsResolver) {
	typeFactsResolvers[lang] = r
}

// UnregisterTypeFacts removes a language's arm, restoring the unregistered
// state exactly.
func UnregisterTypeFacts(lang Language) {
	delete(typeFactsResolvers, lang)
}

// typeFactsFor runs the registered arm for one declaration's language, or
// returns nil when none is registered — leaving the chunk's zero value.
func typeFactsFor(lang Language, declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	r, ok := typeFactsResolvers[lang]
	if !ok {
		return nil
	}
	return r(declNode, chunkType, src)
}
