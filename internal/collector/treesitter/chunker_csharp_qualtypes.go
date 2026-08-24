// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterCSharpQualifierTypes()
}

// RegisterCSharpQualifierTypes installs the csharp qualifier-type arm, exported
// for the restore-not-delete reason the Go registrar documents.
func RegisterCSharpQualifierTypes() {
	RegisterQualifierTypes(LangCSharp, csharpQualifierTypes)
}

// csharpNominalSpec is built once per process.
//
// C# PUTS THE TYPE BEFORE THE NAME, and its bind sites locate the type
// POSITIONALLY rather than by class — a parameter's type and its name are BOTH
// plain identifiers, so no class distinguishes them and only their order does.
var csharpNominalSpec = sync.OnceValue(func() *nominalSpec {
	spec := &nominalSpec{
		kinds:           csharpKinds(),
		selfToken:       "this",
		selfSuppressors: []string{"static"},
		typeText:        csharpQualTypeText,
	}
	spec.roles[csharpKindIdentifier] = nominalRoleName
	spec.roles[csharpKindVariableDeclarator] = nominalRoleDeclarator
	spec.roles[csharpKindModifier] = nominalRoleIgnored
	spec.roles[csharpKindAttributeList] = nominalRoleIgnored
	for _, class := range []uint8{
		csharpKindClassDeclaration, csharpKindInterfaceDeclaration, csharpKindStructDeclaration,
		csharpKindRecordDeclaration, csharpKindEnumDeclaration, csharpKindMethodDeclaration,
		csharpKindConstructorDeclaration, csharpKindPropertyDeclaration,
		csharpKindDestructorDeclaration, csharpKindOperatorDeclaration,
		csharpKindIndexerDeclaration, csharpKindEventDeclaration, csharpKindDelegateDeclaration,
	} {
		spec.roles[class] = nominalRoleScopeBreak
	}
	spec.sites[csharpKindParameter] = nominalSiteTypeFirst
	// ONE SITE COVERS FIELDS AND LOCALS ALIKE: a field declaration and a local
	// declaration statement both wrap the same variable_declaration node, so the
	// walk finds it under either without a second rule.
	spec.sites[csharpKindVariableDeclaration] = nominalSiteTypeFirst
	for _, class := range []uint8{
		csharpKindClassDeclaration, csharpKindInterfaceDeclaration,
		csharpKindStructDeclaration, csharpKindRecordDeclaration,
	} {
		spec.containers[class] = true
	}
	return spec
})

// csharpQualifierTypes is the csharp arm: one walk of a declaration's own
// subtree.
func csharpQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	return nominalQualifierTypes(csharpNominalSpec(), declNode, src)
}

// csharpQualTypeText renders a csharp type expression as the text a QUALIFIER's
// type is recorded under, or "" to decline it.
//
// IT ACCEPTS TWO KINDS: a bare user type — which this grammar spells as a plain
// identifier — and a dotted one, whose qualifier is retained.
//
// A PREDEFINED TYPE DECLINES, and the reason is not tidiness: `int`, `void` and
// `string` name no in-repo declaration, so binding one would send the rung
// looking up members of a scope that holds nothing. A generic instantiation
// declines for the reason every renderer in this group declines one, while the
// SUPERTYPE renderer keeps the head instead. `var` is declined by the same
// closed allowlist without a special case, because an implicitly-typed local
// carries no written type for the arm to record.
func csharpQualTypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch csharpKinds().class(typeNode.Symbol()) {
	case csharpKindIdentifier, csharpKindQualifiedName:
		text := typeNode.Content(src)
		if text == "var" {
			// `var t = x;` spells its implicit type as a plain identifier in
			// this grammar, so the allowlist alone would let it through and bind
			// every implicitly-typed local to a type named "var".
			return ""
		}
		return text
	}
	return ""
}
