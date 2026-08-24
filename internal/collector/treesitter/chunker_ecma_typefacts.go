// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterECMATypeFacts()
}

// RegisterECMATypeFacts installs the typescript, tsx and javascript type-facts
// arms, exported for the same restore-not-delete reason as
// RegisterECMAQualifierTypes.
func RegisterECMATypeFacts() {
	RegisterTypeFacts(LangTypeScript, tsTypeFacts)
	RegisterTypeFacts(LangTSX, tsxTypeFacts)
	RegisterTypeFacts(LangJavaScript, jsTypeFacts)
}

func tsTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	return ecmaTypeFacts(tsKinds(), true, declNode, chunkType, src)
}

func tsxTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	return ecmaTypeFacts(tsxKinds(), true, declNode, chunkType, src)
}

func jsTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	return ecmaTypeFacts(jsKinds(), false, declNode, chunkType, src)
}

// ecmaTypeFacts records an ECMAScript declaration's syntax-visible type facts.
//
// IT SETS IsInterface AND NEVER Sig, and the asymmetry is the whole of this
// arm's coupling to the shared mechanism. IsInterface is THE CONTRACT PREDICATE
// the declared-conformance emitter gates on: a supertype resolving to a
// declaration that does not carry it emits nothing and is counted as a
// non-contract instead. Setting it from a non-Go arm is safe because the
// method-set derivation skips every non-Go record before either of its two
// readers, and it is REQUIRED, because without it a TypeScript `implements`
// clause resolves to an interface the emitter does not recognize as one. Sig is
// the mirror image: it feeds signature-key matching, which the same skip makes
// unreachable from here, so a Sig set by this arm would be a carrier with no
// reader.
//
// typed SEPARATES THE TWO FAMILIES. Fields, Results and IsInterface all read
// type syntax, and javascript declares none — no annotations, no interface
// construct, and JSDoc arriving as one opaque comment token this collector does
// not parse. So the javascript arm records none of the three. WHAT IT DOES
// RECORD IS Conforms: a javascript class writes `extends`, that heritage is
// captured here on the same footing as TypeScript's, and it is the whole of what
// this arm carries for that language.
//
// A JAVASCRIPT CAPTURE EMITS NO EDGE, and that is a property of the language
// rather than of this arm. Emission requires the resolved supertype to be a
// contract, javascript declares no contract construct at all, so every
// javascript heritage resolves to a non-contract or to nothing and is COUNTED
// instead of emitted. The capture is built anyway, because the capture plus the
// counters is the honest record and it becomes live the day the language grows
// a contract notion — but nobody should read an armed javascript row as edges in
// the graph.
func ecmaTypeFacts(classes symbolClasses, typed bool, declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	// The unwrap comes first, for the reason the qualifier arm's does: an
	// exported declaration arrives as the export_statement, and every descent
	// below reads DIRECT children — where the unwrap is load-bearing rather than
	// merely tidy, because no recursion would find the class body through the
	// wrapper.
	decl := unwrapExportedDecl(declNode)

	switch chunkType {
	case "class_declaration", "abstract_class_declaration":
		// The heritage is read for BOTH families — it is the whole of what
		// javascript declares — while the fields are typed-only.
		facts := TypeFacts{Conforms: ecmaClassHeritage(classes, decl, src)}
		if typed {
			facts.Fields = ecmaClassFieldTypes(classes, decl, src)
		}
		if len(facts.Conforms) == 0 && len(facts.Fields) == 0 {
			return nil
		}
		return &facts

	case "interface_declaration":
		if !typed {
			return nil
		}
		// A TypeScript interface is the language's contract construct, and the
		// only one it has: a class, an abstract class and a type alias are all
		// concrete under this predicate, whatever they are used for.
		return &TypeFacts{IsInterface: true, Conforms: ecmaInterfaceHeritage(classes, decl, src)}

	case "function_declaration", "method_definition", "method_signature":
		if !typed {
			return nil
		}
		results := ecmaDeclaredResults(classes, decl, src)
		if len(results) == 0 {
			return nil
		}
		return &TypeFacts{Results: results}
	}
	return nil
}

// ecmaClassHeritage captures the supertypes a class declaration declared, with
// the clause each was declared under.
//
// THE TWO FAMILIES PUT THEM IN DIFFERENT PLACES, and reading only the
// TypeScript shape is the single-shape trap this walk is written against. In
// typescript and tsx a class_heritage holds an extends_clause and/or an
// implements_clause and the supertypes sit inside those; in javascript the
// grammar declares NO extends_clause at all — the class_heritage holds the
// anonymous `extends` token and the supertype expression DIRECTLY. So the third
// arm below is not a fallback, it is javascript's only shape, and a walk without
// it captures nothing there while every TypeScript test stays green.
//
// THE KIND ASYMMETRY INSIDE ONE TYPESCRIPT HERITAGE NODE IS REAL: the extends
// clause binds an `identifier` while the implements clause binds
// `type_identifier`s. Neither arm assumes the other's kind.
func ecmaClassHeritage(classes symbolClasses, decl *sitter.Node, src []byte) []DeclaredSupertype {
	heritage := ecmaFirstChildOfClass(classes, decl, ecmaKindClassHeritage)
	if heritage == nil {
		return nil
	}
	var out []DeclaredSupertype
	for i := range int(heritage.NamedChildCount()) {
		clause := heritage.NamedChild(i)
		switch classes.class(clause.Symbol()) {
		case ecmaKindImplementsClause:
			out = ecmaAppendSupertypes(classes, clause, ConformImplements, src, out)
		case ecmaKindExtendsClause:
			out = ecmaAppendSupertypes(classes, clause, ConformExtends, src, out)
		default:
			// javascript: the supertype expression hangs off the heritage node
			// itself. A shape this is not a name — a mixin call — renders as the
			// empty text and is dropped by the append.
			out = ecmaAppendSupertype(classes, clause, ConformExtends, src, out)
		}
	}
	return out
}

// ecmaInterfaceHeritage captures an interface's supertypes.
//
// AN INTERFACE'S CLAUSE IS extends_type_clause, NOT extends_clause — a distinct
// regular symbol in both grammars — so a descent written for the class shape
// finds nothing on an interface while every class test stays green. That is the
// same single-shape trap the class walk above guards against, one grammar level
// down.
func ecmaInterfaceHeritage(classes symbolClasses, decl *sitter.Node, src []byte) []DeclaredSupertype {
	clause := ecmaFirstChildOfClass(classes, decl, ecmaKindExtendsTypeClause)
	if clause == nil {
		return nil
	}
	return ecmaAppendSupertypes(classes, clause, ConformExtends, src, nil)
}

// ecmaAppendSupertypes appends every named supertype a clause carries.
func ecmaAppendSupertypes(
	classes symbolClasses, clause *sitter.Node, kind ConformanceKind, src []byte, out []DeclaredSupertype,
) []DeclaredSupertype {
	for i := range int(clause.NamedChildCount()) {
		out = ecmaAppendSupertype(classes, clause.NamedChild(i), kind, src, out)
	}
	return out
}

// ecmaAppendSupertype appends one supertype, or nothing when its spelling is not
// a name this capture can carry.
func ecmaAppendSupertype(
	classes symbolClasses, node *sitter.Node, kind ConformanceKind, src []byte, out []DeclaredSupertype,
) []DeclaredSupertype {
	text := ecmaSupertypeText(classes, node, src)
	if text == "" {
		return out
	}
	return append(out, DeclaredSupertype{Text: text, Kind: kind})
}

// ecmaSupertypeText renders a supertype's spelling AS WRITTEN under the
// carrier's normalization contract, or "" to decline it.
//
// TYPE ARGUMENTS ARE STRIPPED AND THE QUALIFIER IS RETAINED, so
// `implements Ns.Other<T>` records exactly `Ns.Other`. Both halves matter and
// they pull in opposite directions: taking the node's whole text would record
// the type arguments, which resolution can never match; taking the last segment
// would discard the qualifier, which is the parser's only input for binding the
// name to the DECLARING file's scope.
//
// A HERITAGE EXPRESSION THAT IS NOT A NAME DECLINES RATHER THAN GUESSING. The
// case that forces the rule is the mixin form `extends Mixin(Base)`, which the
// grammar renders as a call_expression: an arm that recorded the callee would
// claim conformance to the FACTORY, and one that reached past it into the
// arguments would claim conformance to a base the source never named as its
// supertype. Both are fabrications, and a dropped capture is the honest answer.
func ecmaSupertypeText(classes symbolClasses, node *sitter.Node, src []byte) string {
	switch classes.class(node.Symbol()) {
	case ecmaKindTypeIdentifier, ecmaKindNestedTypeIdentifier,
		ecmaKindIdentifier, ecmaKindMemberExpression:
		return node.Content(src)
	case ecmaKindGenericType:
		// The base type is the first named child and type_arguments is its
		// SIBLING, so re-entering here strips the arguments without any text
		// surgery — and still declines a base this allowlist does not admit.
		if node.NamedChildCount() > 0 {
			return ecmaSupertypeText(classes, node.NamedChild(0), src)
		}
	}
	return ""
}

// ecmaDeclaredResults returns a declaration's return type as a ONE-ELEMENT
// slice, or nil when it declares no return annotation at all.
//
// POSITION IS LOAD-BEARING, so a return type this arm DECLINES is recorded as
// the EMPTY STRING to hold its slot rather than dropped — the contract
// TypeFacts.Results documents, and the reason a declining result is not the same
// as an absent one. The scan is over DIRECT children only, which is what keeps a
// parameter's own type_annotation, nested inside formal_parameters, from being
// read as the declaration's result.
func ecmaDeclaredResults(classes symbolClasses, decl *sitter.Node, src []byte) []string {
	for i := range int(decl.NamedChildCount()) {
		child := decl.NamedChild(i)
		if classes.class(child.Symbol()) != ecmaKindTypeAnnotation {
			continue
		}
		return []string{ecmaAnnotationTypeIn(classes, child, src)}
	}
	return nil
}

// ecmaClassFieldTypes maps a class's declared field names to their annotated
// types, from BOTH of the two sources TypeScript spells a field with.
//
// NEITHER SOURCE SUBSUMES THE OTHER, which is why both are read here and why
// each has its own end-to-end test. A plain `store: Store` in the class body is
// a public_field_definition; a `constructor(private store: Store)` declares the
// very same field through a parameter property and produces no field definition
// node at all. An arm reading only one of them still satisfies any single-shape
// fixture while silently halving the field hop's reach on real code.
func ecmaClassFieldTypes(classes symbolClasses, decl *sitter.Node, src []byte) map[string]string {
	body := ecmaFirstChildOfClass(classes, decl, ecmaKindClassBody)
	if body == nil {
		return nil
	}
	var fields map[string]string
	put := func(name, text string) {
		// Fields is keyed by NAME, so a declining field is OMITTED: to a map
		// reader an absent entry and an empty one mean the same thing, and the
		// carrier's own contract says to drop rather than hold a slot.
		if name == "" || text == "" {
			return
		}
		if fields == nil {
			fields = map[string]string{}
		}
		fields[name] = text
	}

	for i := range int(body.NamedChildCount()) {
		member := body.NamedChild(i)
		switch classes.class(member.Symbol()) {
		case ecmaKindPublicFieldDefinition:
			name := ecmaFirstChildOfClass(classes, member, ecmaKindPropertyIdentifier)
			annotation := ecmaFirstChildOfClass(classes, member, ecmaKindTypeAnnotation)
			if name == nil || annotation == nil {
				continue
			}
			put(name.Content(src), ecmaAnnotationTypeIn(classes, annotation, src))

		case ecmaKindMethodDefinition:
			name := ecmaFirstChildOfClass(classes, member, ecmaKindPropertyIdentifier)
			if name == nil || name.Content(src) != "constructor" {
				continue
			}
			ecmaParameterProperties(classes, member, src, put)
		}
	}
	return fields
}

// ecmaParameterProperties reports each constructor parameter that also declares
// a field — the ones carrying an accessibility modifier or `readonly`.
//
// THE TWO MARKERS ARE READ DIFFERENTLY BECAUSE THE GRAMMAR SPELLS THEM
// DIFFERENTLY. An accessibility modifier is a NAMED child, so the class table
// sees it; `readonly` is an ANONYMOUS token, which the class table cannot see at
// all — a kind map naming it would panic at first use, since the grammar
// declares no regular symbol under that name. The anonymous form is therefore
// read with the shared child-token probe instead.
// formal_parameters carries no class of its own — the parameter kinds are what
// the scan keys on — so the walk is over the constructor's direct children and
// then over theirs.
func ecmaParameterProperties(classes symbolClasses, ctor *sitter.Node, src []byte, put func(name, text string)) {
	for i := range int(ctor.NamedChildCount()) {
		group := ctor.NamedChild(i)
		for j := range int(group.NamedChildCount()) {
			param := group.NamedChild(j)
			switch classes.class(param.Symbol()) {
			case ecmaKindRequiredParameter, ecmaKindOptionalParameter:
			default:
				continue
			}
			declaresField := ecmaFirstChildOfClass(classes, param, ecmaKindAccessibilityModifier) != nil ||
				hasAnonymousChild(param, "readonly")
			if !declaresField {
				continue
			}
			name := ecmaFirstChildOfClass(classes, param, ecmaKindIdentifier)
			annotation := ecmaFirstChildOfClass(classes, param, ecmaKindTypeAnnotation)
			if name == nil || annotation == nil {
				continue
			}
			put(name.Content(src), ecmaAnnotationTypeIn(classes, annotation, src))
		}
	}
}

// ecmaAnnotationTypeIn renders the type inside a type_annotation under the same
// closed allowlist the qualifier arm applies, so a text recorded here is one
// resolution could actually match.
//
// ONE DISCIPLINE, BOTH PATHS. A text recorded verbatim would look harmless
// because resolution simply fails to find it — but a populated carrier holding
// an unmatchable name is indistinguishable from a real lookup miss, and nothing
// downstream re-parses these texts to strip them.
func ecmaAnnotationTypeIn(classes symbolClasses, annotation *sitter.Node, src []byte) string {
	b := &qualBinder{classes: classes}
	return ecmaAnnotationType(b, annotation, src)
}

// ecmaFirstChildOfClass returns a node's first direct named child of one class,
// or nil.
func ecmaFirstChildOfClass(classes symbolClasses, node *sitter.Node, class uint8) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		if child := node.NamedChild(i); classes.class(child.Symbol()) == class {
			return child
		}
	}
	return nil
}
