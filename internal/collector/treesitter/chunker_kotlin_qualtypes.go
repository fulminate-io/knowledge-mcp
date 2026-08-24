// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterKotlinQualifierTypes()
}

// RegisterKotlinQualifierTypes installs the kotlin qualifier-type arm. It is
// EXPORTED for the restore-not-delete reason the Go registrar documents: an
// arm-off baseline run must be able to put the production arm back.
func RegisterKotlinQualifierTypes() {
	RegisterQualifierTypes(LangKotlin, kotlinQualifierTypes)
}

// kotlinNominalSpec is built once per process.
//
// KOTLIN PUTS THE TYPE AFTER THE NAME, so its bind sites locate the type BY
// CLASS rather than by position: a property carries a trailing initializer
// expression, and a last-child rule would read that initializer as the type.
var kotlinNominalSpec = sync.OnceValue(func() *nominalSpec {
	spec := &nominalSpec{
		kinds:     kotlinKinds(),
		selfToken: "this",
		typeText:  kotlinQualTypeText,
	}
	spec.roles[kotlinKindSimpleIdentifier] = nominalRoleName
	spec.roles[kotlinKindUserType] = nominalRoleType
	spec.roles[kotlinKindModifiers] = nominalRoleIgnored
	spec.roles[kotlinKindBindingPatternKind] = nominalRoleIgnored
	for _, class := range []uint8{
		kotlinKindClassDeclaration, kotlinKindObjectDeclaration, kotlinKindFunctionDeclaration,
	} {
		spec.roles[class] = nominalRoleScopeBreak
	}
	spec.sites[kotlinKindParameter] = nominalSiteNameFirst
	spec.sites[kotlinKindClassParameter] = nominalSiteNameFirst
	spec.sites[kotlinKindVariableDeclaration] = nominalSiteNameFirst
	spec.containers[kotlinKindClassDeclaration] = true
	spec.containers[kotlinKindObjectDeclaration] = true
	return spec
})

// kotlinQualifierTypes is the kotlin arm: one walk of a declaration's own
// subtree.
//
// A `val`/`var` CLASS PARAMETER IS ALSO A PROPERTY, and it binds on the CLASS
// declaration's single invocation like any other field — the constructor is
// part of the class's own subtree, not a separate declaration chunk.
func kotlinQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	return nominalQualifierTypes(kotlinNominalSpec(), declNode, src)
}

// kotlinQualTypeText renders a kotlin type expression as the text a QUALIFIER's
// type is recorded under, or "" to decline it.
//
// IT ACCEPTS ONE KIND: a user type, whose spelling carries its own dotted
// qualifier verbatim. A nullable type, a function type and a parenthesized type
// all decline by default.
//
// A GENERIC INSTANTIATION DECLINES, and the decline is the rule. A qualifier
// typed `List<Store>` names a CONTAINER: the methods reachable through it are
// the container's, and a renderer that descended to the element would hand the
// qualifier methods its value does not have. The SUPERTYPE renderer in the
// conformance arm keeps the head instead, because a supertype spelling names
// the contract itself and its carrier's contract strips type arguments.
func kotlinQualTypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil || kotlinKinds().class(typeNode.Symbol()) != kotlinKindUserType {
		return ""
	}
	for i := range int(typeNode.NamedChildCount()) {
		if kotlinKinds().class(typeNode.NamedChild(i).Symbol()) == kotlinKindTypeArguments {
			return ""
		}
	}
	return typeNode.Content(src)
}
