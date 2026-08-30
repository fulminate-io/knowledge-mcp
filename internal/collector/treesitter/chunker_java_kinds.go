// SPDX-License-Identifier: Apache-2.0

package treesitter

// The java kind classes. javaKindOther is the ZERO VALUE and therefore the
// class of every symbol the table does not name, which is what makes an
// unclassified symbol behave like a kind no arm matches rather than a wrong one.
//
// NO ANONYMOUS TOKEN MAY BE NAMED HERE. The class-table builder walks REGULAR
// symbols only and then panics on any kinds-map name that received no
// assignment, so naming a keyword panics this language's table at first use
// rather than at compile time. Java's conformance clauses each carry a NAMED
// wrapper node, so this language needs no keyword lookup at all.
const (
	javaKindOther uint8 = iota
	javaKindIdentifier
	javaKindTypeIdentifier
	javaKindScopedTypeIdentifier
	javaKindGenericType
	javaKindArrayType
	javaKindModifiers
	javaKindVariableDeclarator
	javaKindFormalParameter
	javaKindLocalVariableDeclaration
	javaKindFieldDeclaration
	javaKindClassDeclaration
	javaKindInterfaceDeclaration
	javaKindEnumDeclaration
	javaKindRecordDeclaration
	javaKindAnnotationTypeDeclaration
	javaKindMethodDeclaration
	javaKindConstructorDeclaration
	javaKindTypeList
	javaKindSuperInterfaces
	javaKindExtendsInterfaces
	javaKindClassBody
	javaKindInterfaceBody
	javaKindFormalParameters
	javaKindMethodInvocation
	javaKindArgumentList
	javaKindAssignmentExpression
	javaKindFieldAccess
	javaKindReturnStatement
	javaKindLambdaExpression
)

// javaKindNames maps every java node-kind spelling the java arms name onto its
// class code.
var javaKindNames = map[string]uint8{
	"identifier":                  javaKindIdentifier,
	"type_identifier":             javaKindTypeIdentifier,
	"scoped_type_identifier":      javaKindScopedTypeIdentifier,
	"generic_type":                javaKindGenericType,
	"array_type":                  javaKindArrayType,
	"modifiers":                   javaKindModifiers,
	"variable_declarator":         javaKindVariableDeclarator,
	"formal_parameter":            javaKindFormalParameter,
	"local_variable_declaration":  javaKindLocalVariableDeclaration,
	"field_declaration":           javaKindFieldDeclaration,
	"class_declaration":           javaKindClassDeclaration,
	"interface_declaration":       javaKindInterfaceDeclaration,
	"enum_declaration":            javaKindEnumDeclaration,
	"record_declaration":          javaKindRecordDeclaration,
	"annotation_type_declaration": javaKindAnnotationTypeDeclaration,
	"method_declaration":          javaKindMethodDeclaration,
	"constructor_declaration":     javaKindConstructorDeclaration,
	"type_list":                   javaKindTypeList,
	"super_interfaces":            javaKindSuperInterfaces,
	"extends_interfaces":          javaKindExtendsInterfaces,
	"class_body":                  javaKindClassBody,
	"interface_body":              javaKindInterfaceBody,
	"formal_parameters":           javaKindFormalParameters,
	"method_invocation":           javaKindMethodInvocation,
	"argument_list":               javaKindArgumentList,
	"assignment_expression":       javaKindAssignmentExpression,
	"field_access":                javaKindFieldAccess,
	"return_statement":            javaKindReturnStatement,
	"lambda_expression":           javaKindLambdaExpression,
}

// javaKindTable memoizes the java class table for the process, on the shared
// lazy-build type rather than a sync.Once of this file's own.
var javaKindTable = kindTable{lang: LangJava, names: javaKindNames}

// javaKinds returns the memoized java symbol class table.
func javaKinds() symbolClasses { return javaKindTable.get() }
