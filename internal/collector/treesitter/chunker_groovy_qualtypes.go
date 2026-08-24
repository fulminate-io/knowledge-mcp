// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterGroovyQualifierTypes()
}

// RegisterGroovyQualifierTypes installs the groovy qualifier-type arm, exported
// for the restore-not-delete reason the Go registrar documents.
func RegisterGroovyQualifierTypes() {
	RegisterQualifierTypes(LangGroovy, groovyQualifierTypes)
}

// groovyNominalSpec is built once per process.
//
// GROOVY PUTS THE TYPE BEFORE THE NAME, and its bind sites locate the type
// POSITIONALLY because a declaration's type and its name are BOTH plain
// identifiers. That is also why the untyped marker matters here and nowhere
// else: `def z = other` has no type node between the name and the initializer,
// so a positional rule with no marker would read the NAME as the type and bind
// the initializer's spelling to it.
var groovyNominalSpec = sync.OnceValue(func() *nominalSpec {
	spec := &nominalSpec{
		kinds:           groovyKinds(),
		selfToken:       "this",
		selfSuppressors: []string{"static"},
		untypedMarkers:  []string{"def"},
		typeText:        groovyQualTypeText,
	}
	spec.roles[groovyKindIdentifier] = nominalRoleName
	spec.roles[groovyKindModifier] = nominalRoleIgnored
	spec.roles[groovyKindAccessModifier] = nominalRoleIgnored
	spec.roles[groovyKindAnnotation] = nominalRoleIgnored
	for _, class := range []uint8{
		groovyKindClassDefinition, groovyKindFunctionDefinition, groovyKindFunctionDeclaration,
	} {
		spec.roles[class] = nominalRoleScopeBreak
	}
	spec.sites[groovyKindParameter] = nominalSiteTypeFirst
	spec.sites[groovyKindDeclaration] = nominalSiteTypeFirst
	spec.containers[groovyKindClassDefinition] = true
	return spec
})

// groovyQualifierTypes is the groovy arm: one walk of a declaration's own
// subtree.
func groovyQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	return nominalQualifierTypes(groovyNominalSpec(), declNode, src)
}

// groovyQualTypeText renders a groovy type expression as the text a QUALIFIER's
// type is recorded under, or "" to decline it.
//
// IT ACCEPTS TWO KINDS: a bare user type, which this grammar spells as a plain
// identifier, and a dotted one whose qualifier is retained. A built-in type
// declines because it names no in-repo declaration, and a generic or array type
// declines with everything else the allowlist does not name.
func groovyQualTypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch groovyKinds().class(typeNode.Symbol()) {
	case groovyKindIdentifier, groovyKindDottedIdentifier:
		return typeNode.Content(src)
	}
	return ""
}
