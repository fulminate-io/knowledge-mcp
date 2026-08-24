// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterScalaQualifierTypes()
}

// RegisterScalaQualifierTypes installs the scala qualifier-type arm, exported
// for the restore-not-delete reason the Go registrar documents.
func RegisterScalaQualifierTypes() {
	RegisterQualifierTypes(LangScala, scalaQualifierTypes)
}

// scalaNominalSpec is built once per process.
//
// SCALA PUTS THE TYPE AFTER THE NAME, so its bind sites locate the type BY
// CLASS: a val definition carries a trailing right-hand expression, and a
// last-child rule would read that expression as the declared type.
var scalaNominalSpec = sync.OnceValue(func() *nominalSpec {
	spec := &nominalSpec{
		kinds:     scalaKinds(),
		selfToken: "this",
		typeText:  scalaQualTypeText,
	}
	spec.roles[scalaKindIdentifier] = nominalRoleName
	spec.roles[scalaKindTypeIdentifier] = nominalRoleType
	spec.roles[scalaKindStableTypeIdentifier] = nominalRoleType
	spec.roles[scalaKindModifiers] = nominalRoleIgnored
	spec.roles[scalaKindAccessModifier] = nominalRoleIgnored
	spec.roles[scalaKindAnnotation] = nominalRoleIgnored
	for _, class := range []uint8{
		scalaKindClassDefinition, scalaKindObjectDefinition, scalaKindTraitDefinition,
		scalaKindFunctionDefinition, scalaKindFunctionDeclaration,
	} {
		spec.roles[class] = nominalRoleScopeBreak
	}
	spec.sites[scalaKindParameter] = nominalSiteNameFirst
	spec.sites[scalaKindClassParameter] = nominalSiteNameFirst
	spec.sites[scalaKindValDefinition] = nominalSiteNameFirst
	spec.sites[scalaKindVarDefinition] = nominalSiteNameFirst
	spec.sites[scalaKindValDeclaration] = nominalSiteNameFirst
	spec.sites[scalaKindVarDeclaration] = nominalSiteNameFirst
	for _, class := range []uint8{
		scalaKindClassDefinition, scalaKindObjectDefinition, scalaKindTraitDefinition,
	} {
		spec.containers[class] = true
	}
	return spec
})

// scalaQualifierTypes is the scala arm: one walk of a declaration's own subtree.
//
// A CLASS PARAMETER IS ALSO A FIELD, and it binds on the CLASS declaration's
// single invocation like a template-body val — the parameter list is part of
// the class's own subtree rather than a separate declaration chunk.
func scalaQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	return nominalQualifierTypes(scalaNominalSpec(), declNode, src)
}

// scalaQualTypeText renders a scala type expression as the text a QUALIFIER's
// type is recorded under, or "" to decline it.
//
// IT ACCEPTS TWO KINDS: a bare type name and a stable (dotted) one, whose
// qualifier is retained because the declaring file's imports are what bind it.
// A generic instantiation declines for the reason every renderer in this group
// declines one — a qualifier typed to a container must not be handed the
// element's methods — while the SUPERTYPE renderer keeps the head instead. An
// inferred val declares no type node at all and so binds nothing, which is
// correct: this arm does not infer.
func scalaQualTypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch scalaKinds().class(typeNode.Symbol()) {
	case scalaKindTypeIdentifier, scalaKindStableTypeIdentifier:
		return typeNode.Content(src)
	}
	return ""
}
