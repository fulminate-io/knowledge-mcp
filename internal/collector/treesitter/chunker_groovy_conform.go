// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterGroovyTypeFacts()
}

// RegisterGroovyTypeFacts installs the groovy type-facts arm, exported for the
// same restore-not-delete reason as the qualifier-type registrar.
func RegisterGroovyTypeFacts() {
	RegisterTypeFacts(LangGroovy, groovyTypeFacts)
}

// groovyTypeFacts records a groovy type declaration's declared supertypes,
// whether it is itself a contract, and its field types.
//
// THE CAPTURE BOUNDARY, STATED WHOLE BECAUSE A READER WILL OTHERWISE MISREAD
// GROOVY'S LOW EDGE COUNT AS A DEFECT:
//
//   - The vendored grammar declares NO `implements` token at all.
//   - A combined `extends X implements Y` clause, a bare `implements` clause and
//     any MULTI-supertype `extends` clause each produce a direct ERROR child,
//     and such a declaration is DECLINED rather than salvaged — a recovered
//     parse cannot say which spelling was extends and which was implements, so
//     salvaging would mean guessing a kind and stamping it onto an edge.
//   - Groovy registers no import-binding arm and resolves file-scoped, so a
//     supertype declared in another file cannot resolve.
//   - An extends target that is a concrete class emits nothing, because the
//     declared-conformance contract gate reads the RESOLVED target.
//
// WHAT REMAINS IS SINGLE-SUPERTYPE `extends` ONLY, and the only groovy
// declaration that yields an edge is a single-supertype INTERFACE EXTENDING AN
// INTERFACE. A class extending one class captures its entry and emits nothing.
//
// THE FIELD NAMED `superclass:` IS DELIBERATELY NOT READ, and that is the whole
// reason this arm walks children in order. That field is bound even on shapes
// the grammar cannot parse, and it binds THE WRONG NODE: on
// `class Server extends Base implements Greeter`, the ERROR node covers "Base
// implements" and the parse recovers onto Greeter, so the field yields the
// IMPLEMENTS target. A field-reading arm would capture Greeter under the
// extends kind — and because Greeter resolves to a contract, that fabricates an
// edge whose label contradicts the source, with the real superclass dropped.
func groovyTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	if declNode == nil || chunkType != "class_definition" {
		return nil
	}
	facts := &TypeFacts{
		Conforms:    groovyExtendsSupertypes(declNode, src),
		IsInterface: hasAnonymousChild(declNode, "interface"),
		Fields:      groovyFieldTypes(declNode, src),
	}
	return nominalTypeFacts(facts)
}

// groovyExtendsSupertypes walks a type declaration's children ONCE, in order,
// capturing the supertypes after the `extends` keyword — and declining the
// whole declaration when any direct child is an ERROR node.
//
// THE ERROR TEST AND THE ORDERED WALK SHARE ONE PASS, which is why the decline
// is a direct-node predicate rather than a second helper call over the same
// child list. It is deliberately IsError() on the DIRECT children rather than
// HasError() on the declaration: `class B extends Base { void f( }` — a clean
// clause with a broken body — reports no direct-child error while the
// declaration's subtree does, and declining it would throw away a conformance
// clause that parsed perfectly.
func groovyExtendsSupertypes(declNode *sitter.Node, src []byte) []DeclaredSupertype {
	var out []DeclaredSupertype
	seen := false
	for i := range int(declNode.ChildCount()) {
		child := declNode.Child(i)
		if child.IsError() {
			return nil
		}
		if !child.IsNamed() {
			// The keyword is read from the child's own text, because an
			// anonymous token has no regular symbol for the class table to hold.
			if child.Content(src) == "extends" {
				seen = true
			}
			continue
		}
		if !seen {
			continue
		}
		if groovyKinds().class(child.Symbol()) == groovyKindClosure {
			// The body ends the clause.
			break
		}
		out = nominalDeclaredSupertypes(out, groovyQualTypeText(child, src), ConformExtends)
	}
	return out
}

// groovyFieldTypes maps a declaration's field names to their declared types —
// the carrier the field hop reads.
//
// A field is an ordinary declaration sitting directly in the class's body, so
// the walk reads the body's direct children and descends no further: a
// declaration inside a method is that method's local, not the type's field.
func groovyFieldTypes(declNode *sitter.Node, src []byte) map[string]string {
	var out map[string]string
	for i := range int(declNode.NamedChildCount()) {
		body := declNode.NamedChild(i)
		if groovyKinds().class(body.Symbol()) != groovyKindClosure {
			continue
		}
		for j := range int(body.NamedChildCount()) {
			decl := body.NamedChild(j)
			if groovyKinds().class(decl.Symbol()) != groovyKindDeclaration {
				continue
			}
			name, text := groovyDeclarationParts(decl, src)
			if name == "" || text == "" {
				continue
			}
			if out == nil {
				out = map[string]string{}
			}
			out[name] = text
		}
	}
	return out
}

// groovyDeclarationParts returns a field declaration's declared name and
// rendered type, either of which may be empty.
//
// The type precedes the name and both are plain identifiers, so they are told
// apart by POSITION — and a `def` declaration, which carries no type node at
// all, is declined before the positional rule can read its name as a type.
func groovyDeclarationParts(decl *sitter.Node, src []byte) (string, string) {
	if hasAnonymousChild(decl, "def") {
		return "", ""
	}
	var kids []*sitter.Node
	for i := range int(decl.NamedChildCount()) {
		child := decl.NamedChild(i)
		switch groovyKinds().class(child.Symbol()) {
		case groovyKindModifier, groovyKindAccessModifier, groovyKindAnnotation:
			continue
		}
		kids = append(kids, child)
	}
	if len(kids) < 2 || groovyKinds().class(kids[1].Symbol()) != groovyKindIdentifier {
		return "", ""
	}
	return kids[1].Content(src), groovyQualTypeText(kids[0], src)
}
