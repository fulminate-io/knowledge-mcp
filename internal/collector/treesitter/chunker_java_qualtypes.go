// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

func init() {
	RegisterJavaQualifierTypes()
}

// RegisterJavaQualifierTypes installs the java qualifier-type arm.
//
// It is EXPORTED for the reason the Go registrar is: a test that takes the arm
// OUT to measure an unarmed baseline must be able to put the PRODUCTION arm
// back, and the unregister call deletes the entry rather than parking it. A
// cleanup that only unregistered would silently disarm this rung for every
// later test in the same binary.
func RegisterJavaQualifierTypes() {
	RegisterQualifierTypes(LangJava, javaQualifierTypes)
}

// javaNominalSpec is built once per process. The spec holds the language's
// class table, which the shared table type already memoizes, plus the role and
// site arrays derived from it — deriving those on every declaration would pay
// the same map walk thousands of times per file.
var javaNominalSpec = sync.OnceValue(func() *nominalSpec {
	spec := &nominalSpec{
		kinds:           javaKinds(),
		selfToken:       "this",
		selfSuppressors: []string{"static"},
		typeText:        javaQualTypeText,
	}
	spec.roles[javaKindIdentifier] = nominalRoleName
	spec.roles[javaKindVariableDeclarator] = nominalRoleDeclarator
	spec.roles[javaKindModifiers] = nominalRoleIgnored
	for _, class := range []uint8{
		javaKindClassDeclaration, javaKindInterfaceDeclaration, javaKindEnumDeclaration,
		javaKindRecordDeclaration, javaKindAnnotationTypeDeclaration,
		javaKindMethodDeclaration, javaKindConstructorDeclaration,
	} {
		spec.roles[class] = nominalRoleScopeBreak
	}
	spec.sites[javaKindFormalParameter] = nominalSiteTypeFirst
	spec.sites[javaKindLocalVariableDeclaration] = nominalSiteTypeFirst
	spec.sites[javaKindFieldDeclaration] = nominalSiteTypeFirst
	for _, class := range []uint8{
		javaKindClassDeclaration, javaKindInterfaceDeclaration, javaKindEnumDeclaration,
		javaKindRecordDeclaration, javaKindAnnotationTypeDeclaration,
	} {
		spec.containers[class] = true
	}
	return spec
})

// javaQualifierTypes is the java arm: one walk of a declaration's own subtree.
func javaQualifierTypes(declNode *sitter.Node, src []byte) map[string]QualType {
	return nominalQualifierTypes(javaNominalSpec(), declNode, src)
}

// javaQualTypeText renders a java type expression as the text a QUALIFIER's
// type is recorded under, or "" to decline it.
//
// IT IS A CLOSED ALLOWLIST ACCEPTING TWO KINDS: a bare type name and a
// package-qualified one, with the qualifier retained because binding a name to
// a scope happens against the declaring file's imports and stripping it there
// would destroy the input.
//
// A GENERIC INSTANTIATION AND AN ARRAY BOTH DECLINE, and the decline is the
// rule rather than an omission. A qualifier typed `List<Store> xs` names a
// CONTAINER: the methods reachable through xs are the container's, not the
// element's, and a renderer that descended to the element would hand the
// qualifier methods its value does not have. Declining leaves the reference
// exactly where it was, which is what this rung's bind-only bar requires; a
// wrong bind would not be. The SUPERTYPE renderer in the conformance arm is
// deliberately different — a supertype spelling is stored under a contract that
// strips type arguments and keeps the head — because the two answer different
// questions.
func javaQualTypeText(typeNode *sitter.Node, src []byte) string {
	if typeNode == nil {
		return ""
	}
	switch javaKinds().class(typeNode.Symbol()) {
	case javaKindTypeIdentifier, javaKindScopedTypeIdentifier:
		return typeNode.Content(src)
	}
	return ""
}
