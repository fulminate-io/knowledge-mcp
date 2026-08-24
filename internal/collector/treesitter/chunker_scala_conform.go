// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterScalaTypeFacts()
}

// RegisterScalaTypeFacts installs the scala type-facts arm, exported for the
// same restore-not-delete reason as the qualifier-type registrar.
func RegisterScalaTypeFacts() {
	RegisterTypeFacts(LangScala, scalaTypeFacts)
}

// scalaTypeFacts records a scala definition's declared supertypes, whether it
// is itself a contract, and its field types.
//
// SCALA CAN TELL AN EXTENDS FROM A MIXIN, and this arm does. The `extends` and
// `with` keywords carry no regular grammar symbol, but an anonymous token is an
// ordinary walkable child, so an ordered walk of the clause attributes each
// named supertype to the keyword that most recently preceded it: the first is
// an EXTENDS and every `with` target is a MIXIN. An arm that walked only the
// NAMED children would see three identical type nodes and would have to record
// them all under one kind, discarding information the tree carries.
//
// THE CLAUSE HANGS OFF THE FIELD NAMED `extend`, SINGULAR. A lookup spelled
// `extends` returns nil and the arm captures nothing at all — silently, with no
// error anywhere — which is why the spelling is stated here rather than left to
// be re-derived.
//
// LINEARIZATION IS NOT MODELED, AND THAT IS A STATED BOUNDARY RATHER THAN A
// GAP TO CLOSE LATER. A scala class's effective method set depends on the
// ORDER its mixins are linearized in, and this capture records FIRST-LEVEL
// declared conformance only. The distinction cannot be recovered at syntax
// level: linearization needs the full transitive mixin graph with its order
// semantics, which is a different derivation from reading a clause.
func scalaTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	switch chunkType {
	case "class_definition", "trait_definition", "object_definition":
	default:
		return nil
	}

	facts := &TypeFacts{}
	facts.Conforms = scalaClauseSupertypes(declNode.ChildByFieldName("extend"), src)
	// A TRAIT IS SCALA'S CONTRACT. A class and an object are not, so a supertype
	// resolving to either emits nothing and is counted as a non-contract.
	facts.IsInterface = chunkType == "trait_definition"
	facts.Fields = scalaFieldTypes(declNode, src)
	return nominalTypeFacts(facts)
}

// scalaClauseSupertypes walks an extends clause IN SOURCE ORDER, attributing
// each named supertype to the keyword before it.
func scalaClauseSupertypes(clause *sitter.Node, src []byte) []DeclaredSupertype {
	if clause == nil {
		return nil
	}
	var out []DeclaredSupertype
	kind := ConformExtends
	for i := range int(clause.ChildCount()) {
		child := clause.Child(i)
		if !child.IsNamed() {
			// The keyword is read from the CHILD'S OWN TEXT rather than from the
			// class table, because an anonymous token has no regular symbol for
			// the table to hold.
			switch child.Content(src) {
			case "extends":
				kind = ConformExtends
			case "with":
				kind = ConformMixin
			}
			continue
		}
		out = nominalDeclaredSupertypes(out, scalaSupertypeText(child, src), kind)
	}
	return out
}

// scalaSupertypeText renders a declared supertype's spelling AS WRITTEN with
// its type arguments stripped and its dotted qualifier retained.
func scalaSupertypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch scalaKinds().class(typeNode.Symbol()) {
	case scalaKindTypeIdentifier, scalaKindStableTypeIdentifier:
		return typeNode.Content(src)
	case scalaKindGenericType:
		// The head runs from the node's start to the type-argument list, taken
		// by BYTE RANGE because a stable head's dots are anonymous and
		// rebuilding the spelling from named children alone would drop them.
		for i := range int(typeNode.NamedChildCount()) {
			child := typeNode.NamedChild(i)
			if scalaKinds().class(child.Symbol()) != scalaKindTypeArguments {
				continue
			}
			if child.StartByte() > typeNode.StartByte() {
				return string(src[typeNode.StartByte():child.StartByte()])
			}
			return ""
		}
	}
	return ""
}

// scalaFieldTypes maps a definition's declared field names to their declared
// types — the carrier the field hop reads.
//
// BOTH PLACES A SCALA FIELD CAN BE WRITTEN ARE READ: a class parameter, which
// is also a field, and a val or var in the template body. A member whose type
// is inferred rather than written is OMITTED.
func scalaFieldTypes(declNode *sitter.Node, src []byte) map[string]string {
	var out map[string]string
	add := func(name, text string) {
		if name == "" || text == "" {
			return
		}
		if out == nil {
			out = map[string]string{}
		}
		out[name] = text
	}
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		switch scalaKinds().class(child.Symbol()) {
		case scalaKindClassParameters:
			for j := range int(child.NamedChildCount()) {
				param := child.NamedChild(j)
				if scalaKinds().class(param.Symbol()) != scalaKindClassParameter {
					continue
				}
				add(scalaNamedType(param, src))
			}
		case scalaKindTemplateBody:
			for j := range int(child.NamedChildCount()) {
				member := child.NamedChild(j)
				switch scalaKinds().class(member.Symbol()) {
				case scalaKindValDefinition, scalaKindVarDefinition,
					scalaKindValDeclaration, scalaKindVarDeclaration:
					add(scalaNamedType(member, src))
				}
			}
		}
	}
	return out
}

// scalaNamedType returns a name-then-type node's declared name and rendered
// type, either of which may be empty.
func scalaNamedType(node *sitter.Node, src []byte) (string, string) {
	var name, text string
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch scalaKinds().class(child.Symbol()) {
		case scalaKindIdentifier:
			if name == "" && text == "" {
				name = child.Content(src)
			}
		case scalaKindTypeIdentifier, scalaKindStableTypeIdentifier:
			if text == "" {
				text = scalaQualTypeText(child, src)
			}
		}
	}
	return name, text
}
