// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterJavaTypeFacts()
}

// RegisterJavaTypeFacts installs the java type-facts arm, exported for the same
// restore-not-delete reason as the qualifier-type registrar.
func RegisterJavaTypeFacts() {
	RegisterTypeFacts(LangJava, javaTypeFacts)
}

// javaTypeFacts records a java type declaration's syntax-visible type facts:
// the supertypes it DECLARED, whether it is itself a contract, and its field
// types.
//
// RESULTS AND THE COMPOSED SIGNATURE STAY NIL. This language's conformance is
// DECLARED in a clause the grammar carries, so nothing here is derived by
// comparing signatures, and composing one would feed a derivation this language
// is deliberately outside of.
//
// JAVA WRITES ITS CONFORMANCE IN THREE PLACES, and the third is the one an
// arm keyed on the class side alone would miss:
//   - a class's `superclass` clause, one type, an EXTENDS;
//   - a class's `interfaces` clause, a type list, each an IMPLEMENTS;
//   - an INTERFACE's own extends clause, which binds no field at all and hangs
//     its types off a differently-kinded child — and which is exactly the shape
//     that DOES produce edges, because an interface extending an interface
//     resolves to a contract while a class extending a concrete base does not.
func javaTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	facts := &TypeFacts{}
	switch chunkType {
	case "class_declaration":
		if sup := declNode.ChildByFieldName("superclass"); sup != nil {
			for i := range int(sup.NamedChildCount()) {
				facts.Conforms = nominalDeclaredSupertypes(facts.Conforms,
					javaSupertypeText(sup.NamedChild(i), src), ConformExtends)
			}
		}
		facts.Conforms = javaAppendTypeList(facts.Conforms,
			declNode.ChildByFieldName("interfaces"), ConformImplements, src)
		facts.Fields = javaFieldTypes(declNode, src)
	case "interface_declaration":
		// The interface-side clause binds NO field name, so it is reached as a
		// direct named child by kind rather than through ChildByFieldName.
		for i := range int(declNode.NamedChildCount()) {
			child := declNode.NamedChild(i)
			if javaKinds().class(child.Symbol()) != javaKindExtendsInterfaces {
				continue
			}
			facts.Conforms = javaAppendTypeList(facts.Conforms, child, ConformExtends, src)
		}
		facts.IsInterface = true
	default:
		return nil
	}
	return nominalTypeFacts(facts)
}

// javaAppendTypeList captures every entry of a clause holding a type_list.
//
// The clause node is the wrapper — `super_interfaces` on the class side,
// `extends_interfaces` on the interface side — and the types sit one level
// further down, so a walk of the clause's own named children would find the
// list rather than the types.
func javaAppendTypeList(out []DeclaredSupertype, clause *sitter.Node, kind ConformanceKind, src []byte) []DeclaredSupertype {
	if clause == nil {
		return out
	}
	for i := range int(clause.NamedChildCount()) {
		list := clause.NamedChild(i)
		if javaKinds().class(list.Symbol()) != javaKindTypeList {
			continue
		}
		for j := range int(list.NamedChildCount()) {
			out = nominalDeclaredSupertypes(out, javaSupertypeText(list.NamedChild(j), src), kind)
		}
	}
	return out
}

// javaSupertypeText renders a declared supertype's spelling AS WRITTEN under
// the carrier's normalization contract: type arguments stripped, qualifier and
// any leading separator RETAINED.
//
// IT ACCEPTS A GENERIC INSTANTIATION WHERE THE QUALIFIER RENDERER DECLINES ONE,
// and the difference is the contract rather than an inconsistency. A qualifier
// typed to a container must not be bound at all, because the methods reachable
// through it are the container's; a supertype spelled `Comparable<Store>` names
// the CONTRACT itself, and the head is the name the declaring file's imports
// bind. Dropping it would silently lose every generic supertype.
func javaSupertypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch javaKinds().class(typeNode.Symbol()) {
	case javaKindTypeIdentifier, javaKindScopedTypeIdentifier:
		return typeNode.Content(src)
	case javaKindGenericType:
		// The head is the FIRST named child; the type_arguments node is its
		// sibling, so taking the head is what strips the arguments.
		if typeNode.NamedChildCount() > 0 {
			return javaSupertypeText(typeNode.NamedChild(0), src)
		}
	}
	return ""
}

// javaFieldTypes maps a class's declared field names to their declared types.
//
// A FIELD WHOSE TYPE DECLINES IS OMITTED: this map is keyed by name, so an
// absent entry and an empty one mean the same thing to its reader. It is the
// carrier the field hop reads, which is how `this.f.go()` reaches a field's
// type from inside a method whose own qualifier map holds no fields.
func javaFieldTypes(declNode *sitter.Node, src []byte) map[string]string {
	var out map[string]string
	for i := range int(declNode.NamedChildCount()) {
		body := declNode.NamedChild(i)
		if javaKinds().class(body.Symbol()) != javaKindClassBody {
			continue
		}
		for j := range int(body.NamedChildCount()) {
			field := body.NamedChild(j)
			if javaKinds().class(field.Symbol()) != javaKindFieldDeclaration {
				continue
			}
			text, names := javaFieldParts(field, src)
			if text == "" {
				continue
			}
			for _, name := range names {
				if out == nil {
					out = map[string]string{}
				}
				out[name] = text
			}
		}
	}
	return out
}

// javaFieldParts returns one field declaration's rendered type and the names it
// declares. `Store a, b;` declares two names against one type, so every
// declarator is read rather than the first alone.
func javaFieldParts(field *sitter.Node, src []byte) (string, []string) {
	var typeNode *sitter.Node
	var names []string
	for i := range int(field.NamedChildCount()) {
		child := field.NamedChild(i)
		switch javaKinds().class(child.Symbol()) {
		case javaKindModifiers:
			continue
		case javaKindVariableDeclarator:
			for j := range int(child.NamedChildCount()) {
				inner := child.NamedChild(j)
				if javaKinds().class(inner.Symbol()) == javaKindIdentifier {
					names = append(names, inner.Content(src))
					break
				}
			}
		default:
			if typeNode == nil {
				typeNode = child
			}
		}
	}
	return javaQualTypeText(typeNode, src), names
}
