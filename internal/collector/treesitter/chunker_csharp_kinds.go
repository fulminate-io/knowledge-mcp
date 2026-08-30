// SPDX-License-Identifier: Apache-2.0

package treesitter

// The csharp kind classes. csharpKindOther is the ZERO VALUE and therefore the
// class of every symbol the table does not name.
//
// NO ANONYMOUS TOKEN MAY BE NAMED HERE. C# needs no keyword lookup anyway: its
// base list carries a NAMED wrapper and its only anonymous token is the shared
// ':' — which is also precisely why the clause cannot say whether an entry is a
// class or a contract.
//
// A KIND NAME MAPS TO A SET OF SYMBOLS, NEVER TO ONE, and this grammar is a
// worked example: it declares base_list at more than one regular symbol id. The
// table builder assigns EVERY symbol matching a name rather than the first, so
// the multiplicity is handled without anything here naming an id.
const (
	csharpKindOther uint8 = iota
	csharpKindIdentifier
	csharpKindQualifiedName
	csharpKindGenericName
	csharpKindPredefinedType
	csharpKindTypeArgumentList
	csharpKindModifier
	csharpKindAttributeList
	csharpKindVariableDeclarator
	csharpKindVariableDeclaration
	csharpKindParameter
	csharpKindFieldDeclaration
	csharpKindDeclarationList
	csharpKindBaseList
	csharpKindClassDeclaration
	csharpKindInterfaceDeclaration
	csharpKindStructDeclaration
	csharpKindRecordDeclaration
	csharpKindEnumDeclaration
	csharpKindMethodDeclaration
	csharpKindConstructorDeclaration
	csharpKindPropertyDeclaration
	csharpKindDestructorDeclaration
	csharpKindOperatorDeclaration
	csharpKindIndexerDeclaration
	csharpKindEventDeclaration
	csharpKindDelegateDeclaration
	csharpKindParameterList
	csharpKindInvocationExpression
	csharpKindMemberAccessExpression
	csharpKindArgumentList
	csharpKindArgument
	csharpKindAssignmentExpression
	csharpKindReturnStatement
	csharpKindLocalDeclarationStatement
	csharpKindLambdaExpression
)

// csharpKindNames maps every csharp node-kind spelling the csharp arms name
// onto its class code.
var csharpKindNames = map[string]uint8{
	"identifier":                  csharpKindIdentifier,
	"qualified_name":              csharpKindQualifiedName,
	"generic_name":                csharpKindGenericName,
	"predefined_type":             csharpKindPredefinedType,
	"type_argument_list":          csharpKindTypeArgumentList,
	"modifier":                    csharpKindModifier,
	"attribute_list":              csharpKindAttributeList,
	"variable_declarator":         csharpKindVariableDeclarator,
	"variable_declaration":        csharpKindVariableDeclaration,
	"parameter":                   csharpKindParameter,
	"field_declaration":           csharpKindFieldDeclaration,
	"declaration_list":            csharpKindDeclarationList,
	"base_list":                   csharpKindBaseList,
	"class_declaration":           csharpKindClassDeclaration,
	"interface_declaration":       csharpKindInterfaceDeclaration,
	"struct_declaration":          csharpKindStructDeclaration,
	"record_declaration":          csharpKindRecordDeclaration,
	"enum_declaration":            csharpKindEnumDeclaration,
	"method_declaration":          csharpKindMethodDeclaration,
	"constructor_declaration":     csharpKindConstructorDeclaration,
	"property_declaration":        csharpKindPropertyDeclaration,
	"destructor_declaration":      csharpKindDestructorDeclaration,
	"operator_declaration":        csharpKindOperatorDeclaration,
	"indexer_declaration":         csharpKindIndexerDeclaration,
	"event_declaration":           csharpKindEventDeclaration,
	"delegate_declaration":        csharpKindDelegateDeclaration,
	"parameter_list":              csharpKindParameterList,
	"invocation_expression":       csharpKindInvocationExpression,
	"member_access_expression":    csharpKindMemberAccessExpression,
	"argument_list":               csharpKindArgumentList,
	"argument":                    csharpKindArgument,
	"assignment_expression":       csharpKindAssignmentExpression,
	"return_statement":            csharpKindReturnStatement,
	"local_declaration_statement": csharpKindLocalDeclarationStatement,
	"lambda_expression":           csharpKindLambdaExpression,
}

// csharpKindTable memoizes the csharp class table for the process, on the
// shared lazy-build type rather than a sync.Once of this file's own.
var csharpKindTable = kindTable{lang: LangCSharp, names: csharpKindNames}

// csharpKinds returns the memoized csharp symbol class table.
func csharpKinds() symbolClasses { return csharpKindTable.get() }
