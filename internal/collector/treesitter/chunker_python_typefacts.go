// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterPythonTypeFacts()
}

// RegisterPythonTypeFacts installs the python type-facts arm, exported for the
// restore-not-delete reason RegisterGoTypeFacts states.
func RegisterPythonTypeFacts() {
	RegisterTypeFacts(LangPython, pythonTypeFacts)
}

// pythonTypeFacts records a python declaration's syntax-visible type facts: a
// class's annotated attributes and its declared bases, and a function's declared
// return type.
//
// IT SETS IsInterface AND NEVER Sig, on the same reasoning the ECMAScript arm
// documents: IsInterface is the contract predicate the declared-conformance
// emitter gates on, and Sig feeds signature-key matching that the method-set
// derivation's non-Go skip makes unreachable from here.
func pythonTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	classes := pyKinds()

	switch chunkType {
	case "class_definition":
		bases := pythonBaseList(classes, declNode)
		facts := TypeFacts{
			Fields:      pythonAttributeTypes(classes, declNode, src),
			Conforms:    pythonDeclaredBases(classes, bases, src),
			IsInterface: pythonIsContract(classes, bases, src),
		}
		if len(facts.Fields) == 0 && len(facts.Conforms) == 0 && !facts.IsInterface {
			return nil
		}
		return &facts

	case "function_definition":
		results := pythonDeclaredResults(classes, declNode, src)
		if len(results) == 0 {
			return nil
		}
		return &TypeFacts{Results: results}
	}
	return nil
}

// pythonDeclaredResults returns a function's `-> T` return type as a ONE-ELEMENT
// slice, or nil when it declares none.
//
// POSITION IS LOAD-BEARING, so a return type this arm DECLINES is recorded as
// the EMPTY STRING to hold its slot rather than dropped. The scan is over DIRECT
// children, which is what keeps an annotated PARAMETER's own type node — nested
// inside `parameters` — from being read as the function's result.
func pythonDeclaredResults(classes symbolClasses, decl *sitter.Node, src []byte) []string {
	typeNode := pythonFirstChildOfClass(classes, decl, pyKindType)
	if typeNode == nil {
		return nil
	}
	return []string{pythonTypeText(classes, typeNode, src)}
}

// pythonAttributeTypes maps a class's annotated attribute names to their
// declared types.
//
// THIS IS WHAT MAKES `self.store.get()` RESOLVE. The reference splits at the last
// separator into the base `self` and the field `store`; the qualifier arm's
// receiver route resolves `self` to the class, and the field hop then reads this
// map for `store`.
func pythonAttributeTypes(classes symbolClasses, decl *sitter.Node, src []byte) map[string]string {
	body := pythonFirstChildOfClass(classes, decl, pyKindBlock)
	if body == nil {
		return nil
	}
	var fields map[string]string
	for i := range int(body.NamedChildCount()) {
		stmt := body.NamedChild(i)
		if classes.class(stmt.Symbol()) != pyKindExpressionStatement {
			continue
		}
		assign := pythonFirstChildOfClass(classes, stmt, pyKindAssignment)
		if assign == nil || assign.NamedChildCount() == 0 {
			continue
		}
		name := assign.NamedChild(0)
		typeNode := pythonFirstChildOfClass(classes, assign, pyKindType)
		if classes.class(name.Symbol()) != pyKindIdentifier || typeNode == nil {
			continue
		}
		text := pythonTypeText(classes, typeNode, src)
		if text == "" {
			// Fields is keyed by NAME, so a declining attribute is OMITTED: an
			// absent entry and an empty one mean the same thing to a map reader.
			continue
		}
		if fields == nil {
			fields = map[string]string{}
		}
		fields[name.Content(src)] = text
	}
	return fields
}

// pythonBaseList returns a class's argument_list, or nil when it declares none.
//
// BOTH SHAPES EXIST AND ONLY ONE IS OBVIOUS: `class Bare:` carries NO
// argument_list at all, while `class Empty()` carries an empty one. A walk that
// assumed the node was always present would nil-dereference on the commonest
// class in any codebase.
func pythonBaseList(classes symbolClasses, decl *sitter.Node) *sitter.Node {
	return pythonFirstChildOfClass(classes, decl, pyKindArgumentList)
}

// pythonDeclaredBases captures a class's nominal bases.
//
// A KEYWORD ARGUMENT IS NOT A SUPERTYPE. `class C(Base, metaclass=Meta)` names
// one base and one construction directive, and recording Meta as a supertype
// would state a conformance the source never declared — so keyword arguments
// decline here even though the metaclass spelling is separately READ by the
// contract predicate, which is a different question about the same node.
//
// A CALL OR SUBSCRIPT BASE DECLINES for the reason a mixin call does elsewhere:
// `class C(Generic[T])` names a parameterised construct rather than a
// declaration this repository could hold.
func pythonDeclaredBases(classes symbolClasses, bases *sitter.Node, src []byte) []DeclaredSupertype {
	if bases == nil {
		return nil
	}
	var out []DeclaredSupertype
	for i := range int(bases.NamedChildCount()) {
		base := bases.NamedChild(i)
		switch classes.class(base.Symbol()) {
		case pyKindIdentifier, pyKindAttribute:
			out = append(out, DeclaredSupertype{Text: base.Content(src), Kind: ConformExtends})
		}
	}
	return out
}

// pythonContractBases are the base spellings that mark the DECLARING class as a
// contract, and pythonContractMetaclasses the metaclass= spellings that do.
//
// BOTH QUALIFIED AND BARE SPELLINGS ARE LISTED because python's import forms make
// both ordinary: `import abc` yields `abc.ABC` while `from abc import ABC` yields
// `ABC`, and the same file may use either.
var (
	pythonContractBases = map[string]bool{
		"ABC": true, "abc.ABC": true,
		"Protocol": true, "typing.Protocol": true,
	}
	pythonContractMetaclasses = map[string]bool{
		"ABCMeta": true, "abc.ABCMeta": true,
	}
)

// pythonIsContract reports the CONTRACT PREDICATE for a python class: its OWN
// base list names an abstract base or a protocol, or carries an abstract
// metaclass.
//
// IT READS THE CLASS'S OWN BASE LIST AND NOTHING ELSE — no transitive walk up
// the inheritance chain. A class inheriting from an in-repo ABC is a concrete
// subclass of one, and marking it a contract too would fan every call resolved
// to it across ITS subclasses.
//
// A STRUCTURAL PROTOCOL IS NOT MATCHED, and that is the ticket's rule rather
// than a limitation of this walk: a class that satisfies a Protocol without
// naming it declares nothing, so there is nothing here to read. Naming Protocol
// as a base does two independent things at once — it makes THIS class a
// contract, and it records Protocol itself as an ordinary captured base — and
// the two must not be confused for one another.
func pythonIsContract(classes symbolClasses, bases *sitter.Node, src []byte) bool {
	if bases == nil {
		return false
	}
	for i := range int(bases.NamedChildCount()) {
		base := bases.NamedChild(i)
		switch classes.class(base.Symbol()) {
		case pyKindIdentifier, pyKindAttribute:
			if pythonContractBases[base.Content(src)] {
				return true
			}
		case pyKindKeywordArgument:
			if pythonMetaclassIsAbstract(classes, base, src) {
				return true
			}
		}
	}
	return false
}

// pythonMetaclassIsAbstract reports whether one keyword argument is a
// `metaclass=` naming an abstract metaclass.
func pythonMetaclassIsAbstract(classes symbolClasses, kw *sitter.Node, src []byte) bool {
	if kw.NamedChildCount() < 2 {
		return false
	}
	name, value := kw.NamedChild(0), kw.NamedChild(1)
	if classes.class(name.Symbol()) != pyKindIdentifier || name.Content(src) != "metaclass" {
		return false
	}
	switch classes.class(value.Symbol()) {
	case pyKindIdentifier, pyKindAttribute:
		return pythonContractMetaclasses[value.Content(src)]
	}
	return false
}
