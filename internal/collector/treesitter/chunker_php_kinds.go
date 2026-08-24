// SPDX-License-Identifier: Apache-2.0

package treesitter

// The php kind classes. phpKindOther is the ZERO VALUE and therefore the class
// of every symbol the table does not name.
//
// NO ANONYMOUS TOKEN MAY BE NAMED HERE. PHP needs no keyword lookup: every one
// of its conformance clauses — the base clause, the interface clause and an
// in-body trait use — is a NAMED node of its own kind, which is what makes PHP
// the one language in this group whose clause kind is fully recoverable.
const (
	phpKindOther uint8 = iota
	phpKindName
	phpKindVariableName
	phpKindNamedType
	phpKindQualifiedName
	phpKindPrimitiveType
	phpKindOptionalType
	phpKindPropertyElement
	phpKindSimpleParameter
	phpKindPropertyDeclaration
	phpKindAssignmentExpression
	phpKindObjectCreationExpression
	phpKindVisibilityModifier
	phpKindStaticModifier
	phpKindReadonlyModifier
	phpKindFinalModifier
	phpKindAbstractModifier
	phpKindVarModifier
	phpKindAttributeList
	phpKindDeclarationList
	phpKindBaseClause
	phpKindClassInterfaceClause
	phpKindUseDeclaration
	phpKindUseList
	phpKindClassDeclaration
	phpKindInterfaceDeclaration
	phpKindTraitDeclaration
	phpKindEnumDeclaration
	phpKindMethodDeclaration
	phpKindFunctionDefinition
)

// phpKindNames maps every php node-kind spelling the php arms name onto its
// class code.
var phpKindNames = map[string]uint8{
	"name":                       phpKindName,
	"variable_name":              phpKindVariableName,
	"named_type":                 phpKindNamedType,
	"qualified_name":             phpKindQualifiedName,
	"primitive_type":             phpKindPrimitiveType,
	"optional_type":              phpKindOptionalType,
	"property_element":           phpKindPropertyElement,
	"simple_parameter":           phpKindSimpleParameter,
	"property_declaration":       phpKindPropertyDeclaration,
	"assignment_expression":      phpKindAssignmentExpression,
	"object_creation_expression": phpKindObjectCreationExpression,
	"visibility_modifier":        phpKindVisibilityModifier,
	"static_modifier":            phpKindStaticModifier,
	"readonly_modifier":          phpKindReadonlyModifier,
	"final_modifier":             phpKindFinalModifier,
	"abstract_modifier":          phpKindAbstractModifier,
	"var_modifier":               phpKindVarModifier,
	"attribute_list":             phpKindAttributeList,
	"declaration_list":           phpKindDeclarationList,
	"base_clause":                phpKindBaseClause,
	"class_interface_clause":     phpKindClassInterfaceClause,
	"use_declaration":            phpKindUseDeclaration,
	"use_list":                   phpKindUseList,
	"class_declaration":          phpKindClassDeclaration,
	"interface_declaration":      phpKindInterfaceDeclaration,
	"trait_declaration":          phpKindTraitDeclaration,
	"enum_declaration":           phpKindEnumDeclaration,
	"method_declaration":         phpKindMethodDeclaration,
	"function_definition":        phpKindFunctionDefinition,
}

// phpKindTable memoizes the php class table for the process, on the shared
// lazy-build type rather than a sync.Once of this file's own.
var phpKindTable = kindTable{lang: LangPHP, names: phpKindNames}

// phpKinds returns the memoized php symbol class table.
func phpKinds() symbolClasses { return phpKindTable.get() }
