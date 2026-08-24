// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterCSharpTypeFacts()
}

// RegisterCSharpTypeFacts installs the csharp type-facts arm, exported for the
// same restore-not-delete reason as the qualifier-type registrar.
func RegisterCSharpTypeFacts() {
	RegisterTypeFacts(LangCSharp, csharpTypeFacts)
}

// csharpTypeFacts records a csharp type declaration's declared supertypes,
// whether it is itself a contract, and its field types.
//
// EVERY BASE-LIST ENTRY IS RECORDED AS UNDECLARED, AND THAT IS THE MEASURED
// TRUTH RATHER THAN A SHORTCUT. A base list's children are plain identifiers
// and dotted names carrying no class-versus-interface marker, and the clause's
// only anonymous token is the shared ':' every entry sits behind. The
// widespread I-prefix is a NAMING CONVENTION, not a grammar fact: an arm that
// read `IFoo` as an interface would state something the tree does not carry,
// and the mistake would ride all the way onto the emitted edge's label.
//
// THE DECLARED KIND IS ONLY THE LABEL; THE RESOLVED KIND IS THE EMISSION GATE.
// C# shows that most clearly of any language here. In `class C : Base, IFoo`
// BOTH entries carry the same undeclared kind, and yet Base emits NOTHING —
// it resolves to a concrete class, whose method IS the callable implementation
// — while IFoo emits, because it resolves to a contract. One clause, two
// entries, identical labels, opposite outcomes.
func csharpTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	switch chunkType {
	case "class_declaration", "interface_declaration", "struct_declaration", "record_declaration":
	default:
		return nil
	}

	facts := &TypeFacts{}
	for i := range int(declNode.NamedChildCount()) {
		clause := declNode.NamedChild(i)
		if csharpKinds().class(clause.Symbol()) != csharpKindBaseList {
			continue
		}
		for j := range int(clause.NamedChildCount()) {
			facts.Conforms = nominalDeclaredSupertypes(facts.Conforms,
				csharpSupertypeText(clause.NamedChild(j), src), ConformUndeclared)
		}
	}
	facts.IsInterface = chunkType == "interface_declaration"
	facts.Fields = csharpFieldTypes(declNode, src)
	facts.PartialBody = csharpIsPartial(declNode)
	return nominalTypeFacts(facts)
}

// csharpIsPartial reports whether a type declaration carries the `partial`
// modifier, which makes it ONE BODY of a type the compiler assembles from
// several.
//
// THE KEYWORD IS AN ANONYMOUS TOKEN INSIDE A NAMED modifier CHILD — `sealed`
// sits in the identical position — so it cannot be named in the symbol-class
// table at all: that table's builder walks REGULAR symbols and panics on a name
// the grammar declares no regular symbol for. Presence is read with the shared
// anonymous-child helper instead, which is also why this file stays free of a
// kind-name comparison of its own.
func csharpIsPartial(declNode *sitter.Node) bool {
	for i := range int(declNode.NamedChildCount()) {
		child := declNode.NamedChild(i)
		if csharpKinds().class(child.Symbol()) != csharpKindModifier {
			continue
		}
		if hasAnonymousChild(child, "partial") {
			return true
		}
	}
	return false
}

// csharpSupertypeText renders a declared supertype's spelling AS WRITTEN with
// its type arguments stripped and its dotted qualifier retained.
func csharpSupertypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch csharpKinds().class(typeNode.Symbol()) {
	case csharpKindIdentifier, csharpKindQualifiedName:
		return typeNode.Content(src)
	case csharpKindGenericName:
		// The head runs from the node's start to the type-argument list, taken
		// by BYTE RANGE so a dotted head keeps its separators.
		for i := range int(typeNode.NamedChildCount()) {
			child := typeNode.NamedChild(i)
			if csharpKinds().class(child.Symbol()) != csharpKindTypeArgumentList {
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

// csharpFieldTypes maps a declaration's field names to their declared types —
// the carrier the field hop reads.
func csharpFieldTypes(declNode *sitter.Node, src []byte) map[string]string {
	var out map[string]string
	for i := range int(declNode.NamedChildCount()) {
		body := declNode.NamedChild(i)
		if csharpKinds().class(body.Symbol()) != csharpKindDeclarationList {
			continue
		}
		for j := range int(body.NamedChildCount()) {
			field := body.NamedChild(j)
			if csharpKinds().class(field.Symbol()) != csharpKindFieldDeclaration {
				continue
			}
			for k := range int(field.NamedChildCount()) {
				decl := field.NamedChild(k)
				if csharpKinds().class(decl.Symbol()) != csharpKindVariableDeclaration {
					continue
				}
				text, names := csharpDeclarationParts(decl, src)
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
	}
	return out
}

// csharpDeclarationParts returns one variable declaration's rendered type and
// the names it declares. `Store a, b;` declares two names against one type, so
// every declarator is read rather than the first alone.
func csharpDeclarationParts(decl *sitter.Node, src []byte) (string, []string) {
	var typeNode *sitter.Node
	var names []string
	for i := range int(decl.NamedChildCount()) {
		child := decl.NamedChild(i)
		if csharpKinds().class(child.Symbol()) == csharpKindVariableDeclarator {
			for j := range int(child.NamedChildCount()) {
				inner := child.NamedChild(j)
				if csharpKinds().class(inner.Symbol()) == csharpKindIdentifier {
					names = append(names, inner.Content(src))
					break
				}
			}
			continue
		}
		if typeNode == nil {
			typeNode = child
		}
	}
	return csharpQualTypeText(typeNode, src), names
}
