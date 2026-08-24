// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterKotlinTypeFacts()
}

// RegisterKotlinTypeFacts installs the kotlin type-facts arm, exported for the
// same restore-not-delete reason as the qualifier-type registrar.
func RegisterKotlinTypeFacts() {
	RegisterTypeFacts(LangKotlin, kotlinTypeFacts)
}

// kotlinTypeFacts records a kotlin type declaration's declared supertypes,
// whether it is itself a contract, and its property types.
//
// KOTLIN WRITES EVERY SUPERTYPE IN ONE CLAUSE AND CLASSIFIES NONE OF THEM BY
// KIND, so the three shapes a delegation specifier can take are what the arm
// reads instead:
//
//   - a CONSTRUCTOR INVOCATION proves a class, because only a class can be
//     constructed — an EXTENDS;
//   - an EXPLICIT DELEGATION (`by`) is an IMPLEMENTS, and that is a rule of the
//     LANGUAGE rather than a fact the tree states: kotlin allows delegation to
//     an interface only;
//   - a BARE user type CANNOT BE ATTRIBUTED. It is legally an interface, and it
//     is also what a class supertype produces when the subclass declares no
//     primary constructor. The grammar cannot tell the two apart, so the entry
//     is recorded as UNDECLARED. Guessing IMPLEMENTS here would state a fact the
//     tree does not carry, and a wrong kind rides all the way onto the edge.
//
// RESULTS AND THE COMPOSED SIGNATURE STAY NIL: this language's conformance is
// declared, so nothing is derived by comparing signatures.
func kotlinTypeFacts(declNode *sitter.Node, chunkType string, src []byte) *TypeFacts {
	if declNode == nil {
		return nil
	}
	switch chunkType {
	case "class_declaration", "object_declaration":
	default:
		return nil
	}

	facts := &TypeFacts{}
	for i := range int(declNode.NamedChildCount()) {
		spec := declNode.NamedChild(i)
		if kotlinKinds().class(spec.Symbol()) != kotlinKindDelegationSpecifier {
			continue
		}
		text, kind := kotlinDelegationEntry(spec, src)
		facts.Conforms = nominalDeclaredSupertypes(facts.Conforms, text, kind)
	}
	// THERE IS NO interface_declaration KIND IN THIS GRAMMAR. `interface I`
	// parses as a class_declaration carrying an anonymous `interface` child, and
	// an anonymous token has no regular symbol — so it is unreachable through
	// the class table and is read with the shared anonymous-child helper.
	facts.IsInterface = hasAnonymousChild(declNode, "interface")
	facts.Fields = kotlinPropertyTypes(declNode, src)
	return nominalTypeFacts(facts)
}

// kotlinDelegationEntry renders one delegation specifier and classifies it.
func kotlinDelegationEntry(spec *sitter.Node, src []byte) (string, ConformanceKind) {
	if spec.NamedChildCount() == 0 {
		return "", ConformUndeclared
	}
	inner := spec.NamedChild(0)
	switch kotlinKinds().class(inner.Symbol()) {
	case kotlinKindConstructorInvocation:
		return kotlinSupertypeText(kotlinFirstUserType(inner), src), ConformExtends
	case kotlinKindExplicitDelegation:
		return kotlinSupertypeText(kotlinFirstUserType(inner), src), ConformImplements
	case kotlinKindUserType:
		return kotlinSupertypeText(inner, src), ConformUndeclared
	}
	return "", ConformUndeclared
}

// kotlinFirstUserType returns a wrapper's first user-type child, or nil.
func kotlinFirstUserType(node *sitter.Node) *sitter.Node {
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		if kotlinKinds().class(child.Symbol()) == kotlinKindUserType {
			return child
		}
	}
	return nil
}

// kotlinSupertypeText renders a supertype's spelling AS WRITTEN with its type
// arguments stripped and its dotted qualifier retained.
//
// THE ARGUMENTS ARE STRIPPED BY BYTE RANGE, not by re-joining the children,
// because the dots between a user type's segments are ANONYMOUS: rebuilding the
// spelling from the named children alone would silently drop them and turn
// `a.b.Base` into `abBase`.
func kotlinSupertypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	end := typeNode.EndByte()
	for i := range int(typeNode.NamedChildCount()) {
		child := typeNode.NamedChild(i)
		if kotlinKinds().class(child.Symbol()) == kotlinKindTypeArguments {
			end = child.StartByte()
			break
		}
	}
	if end <= typeNode.StartByte() || int(end) > len(src) {
		return ""
	}
	return string(src[typeNode.StartByte():end])
}

// kotlinPropertyTypes maps a declaration's property names to their declared
// types — the carrier the field hop reads.
//
// BOTH PLACES A KOTLIN PROPERTY CAN BE WRITTEN ARE READ: a `val`/`var` class
// parameter in the primary constructor, and a property declaration in the class
// body. A property whose type is inferred rather than written is OMITTED, which
// is correct because this arm does not infer.
func kotlinPropertyTypes(declNode *sitter.Node, src []byte) map[string]string {
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
		switch kotlinKinds().class(child.Symbol()) {
		case kotlinKindPrimaryConstructor:
			kotlinConstructorProperties(child, src, add)
		case kotlinKindClassBody:
			kotlinBodyProperties(child, src, add)
		}
	}
	return out
}

// kotlinConstructorProperties reads the primary constructor's PROPERTY
// parameters.
//
// A parameter with NO binding marker is a plain constructor argument rather
// than a property, and recording it as a field would let a field hop reach a
// name the type does not hold.
func kotlinConstructorProperties(ctor *sitter.Node, src []byte, add func(name, text string)) {
	for i := range int(ctor.NamedChildCount()) {
		param := ctor.NamedChild(i)
		if kotlinKinds().class(param.Symbol()) != kotlinKindClassParameter {
			continue
		}
		if !kotlinIsPropertyParameter(param) {
			continue
		}
		add(kotlinNamedType(param, src))
	}
}

// kotlinBodyProperties reads the properties declared in a class body. The
// name-and-type pair sits one level below the property declaration, in its
// variable declaration.
func kotlinBodyProperties(body *sitter.Node, src []byte, add func(name, text string)) {
	for i := range int(body.NamedChildCount()) {
		member := body.NamedChild(i)
		if kotlinKinds().class(member.Symbol()) != kotlinKindPropertyDeclaration {
			continue
		}
		for j := range int(member.NamedChildCount()) {
			decl := member.NamedChild(j)
			if kotlinKinds().class(decl.Symbol()) != kotlinKindVariableDeclaration {
				continue
			}
			add(kotlinNamedType(decl, src))
		}
	}
}

// kotlinIsPropertyParameter reports whether a class parameter carries a
// val/var binding marker, which is what makes it a property.
func kotlinIsPropertyParameter(param *sitter.Node) bool {
	for i := range int(param.NamedChildCount()) {
		if kotlinKinds().class(param.NamedChild(i).Symbol()) == kotlinKindBindingPatternKind {
			return true
		}
	}
	return false
}

// kotlinNamedType returns a name-then-type node's declared name and rendered
// type, either of which may be empty.
func kotlinNamedType(node *sitter.Node, src []byte) (string, string) {
	var name, text string
	for i := range int(node.NamedChildCount()) {
		child := node.NamedChild(i)
		switch kotlinKinds().class(child.Symbol()) {
		case kotlinKindSimpleIdentifier:
			if name == "" {
				name = child.Content(src)
			}
		case kotlinKindUserType:
			if text == "" {
				text = kotlinQualTypeText(child, src)
			}
		}
	}
	return name, text
}
