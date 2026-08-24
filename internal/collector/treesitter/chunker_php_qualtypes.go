// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterPHPQualifierTypes()
}

// RegisterPHPQualifierTypes installs the php qualifier-type arm, exported for
// the restore-not-delete reason the Go registrar documents.
func RegisterPHPQualifierTypes() {
	RegisterQualifierTypes(LangPHP, phpQualifierTypes)
}

// phpNominalSpec is built once per process.
//
// THE KEYS THIS ARM PRODUCES CARRY THE "$" SIGIL, AND THAT IS LOAD-BEARING
// RATHER THAN INCIDENTAL. The composed callee text for `$o->doThing(1)` is the
// literal `$o->doThing`, and the qualifier handed to the resolution rung is
// therefore `$o`, sigil included. A key spelled `o` would match nothing at all.
// The FIELD map produced by the type-facts arm is keyed the other way, without
// the sigil, because `$this->f` accesses member `f` — the property is DECLARED
// `$f` and ACCESSED `f`, and normalizing either spelling into the other breaks
// the field hop in one direction or the other.
//
// PHP LOCALS CARRY NO DECLARED TYPE, so the only local-binding shape available
// is an assignment whose right-hand side constructs an object. It is registered
// as a NAME-FIRST site with the construction in the type position: `$q = new
// Baz()` names the type DIRECTLY, so it is bound as a plain type rather than as
// a callee whose result type would need a second lookup.
var phpNominalSpec = sync.OnceValue(func() *nominalSpec {
	spec := &nominalSpec{
		kinds:           phpKinds(),
		selfToken:       "$this",
		selfSuppressors: []string{"static"},
		typeText:        phpQualTypeText,
	}
	spec.roles[phpKindVariableName] = nominalRoleName
	spec.roles[phpKindPropertyElement] = nominalRoleDeclarator
	spec.roles[phpKindNamedType] = nominalRoleType
	spec.roles[phpKindObjectCreationExpression] = nominalRoleType
	for _, class := range []uint8{
		phpKindVisibilityModifier, phpKindStaticModifier, phpKindReadonlyModifier,
		phpKindFinalModifier, phpKindAbstractModifier, phpKindVarModifier, phpKindAttributeList,
	} {
		spec.roles[class] = nominalRoleIgnored
	}
	for _, class := range []uint8{
		phpKindClassDeclaration, phpKindInterfaceDeclaration, phpKindTraitDeclaration,
		phpKindEnumDeclaration, phpKindMethodDeclaration, phpKindFunctionDefinition,
	} {
		spec.roles[class] = nominalRoleScopeBreak
	}
	spec.sites[phpKindSimpleParameter] = nominalSiteTypeFirst
	spec.sites[phpKindPropertyDeclaration] = nominalSiteTypeFirst
	spec.sites[phpKindAssignmentExpression] = nominalSiteNameFirst
	for _, class := range []uint8{
		phpKindClassDeclaration, phpKindInterfaceDeclaration, phpKindTraitDeclaration,
	} {
		spec.containers[class] = true
	}
	return spec
})

// phpQualifierTypes is the php arm: one walk of a declaration's own subtree.
func phpQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	return nominalQualifierTypes(phpNominalSpec(), declNode, src)
}

// phpQualTypeText renders a php type expression as the text a QUALIFIER's type
// is recorded under, or "" to decline it.
//
// A NAMED TYPE is accepted whole, so a namespace-qualified spelling keeps its
// leading separator and every segment — the declaring file's own use statements
// are what bind it, and stripping the qualifier here would destroy that input.
// A CONSTRUCTION is accepted through its constructed name, which is what makes
// `$q = new Baz()` bind. A primitive type and an optional (nullable) type both
// decline: neither names an in-repo declaration this rung could reach members
// through.
func phpQualTypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch phpKinds().class(typeNode.Symbol()) {
	case phpKindNamedType:
		return typeNode.Content(src)
	case phpKindObjectCreationExpression:
		for i := range int(typeNode.NamedChildCount()) {
			child := typeNode.NamedChild(i)
			switch phpKinds().class(child.Symbol()) {
			case phpKindName, phpKindQualifiedName:
				return child.Content(src)
			}
		}
	}
	return ""
}
