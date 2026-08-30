// SPDX-License-Identifier: Apache-2.0

package treesitter

// The ECMAScript kind classes, shared by the typescript, tsx and javascript
// arms. ecmaKindOther is the ZERO VALUE and therefore the class of every symbol
// a table does not name, which is what lets one const block serve three
// grammars: a kind javascript does not declare simply classifies as Other
// there, and every arm switch already treats Other as "no case matches".
//
// The class codes are shared; the NAME MAPS and the TABLES are not, for two
// separate reasons stated below at each.
const (
	ecmaKindOther uint8 = iota
	ecmaKindClassDeclaration
	ecmaKindAbstractClassDeclaration
	ecmaKindClassExpression
	ecmaKindClassBody
	ecmaKindClassHeritage
	ecmaKindImplementsClause
	ecmaKindExtendsClause
	ecmaKindExtendsTypeClause
	ecmaKindInterfaceDeclaration
	ecmaKindInterfaceBody
	ecmaKindTypeAliasDeclaration
	ecmaKindMethodSignature
	ecmaKindPropertySignature
	ecmaKindMethodDefinition
	ecmaKindFieldDefinition
	ecmaKindPublicFieldDefinition
	ecmaKindTypeAnnotation
	ecmaKindRequiredParameter
	ecmaKindOptionalParameter
	ecmaKindAccessibilityModifier
	ecmaKindVariableDeclarator
	ecmaKindNewExpression
	ecmaKindCallExpression
	ecmaKindMemberExpression
	ecmaKindTypeIdentifier
	ecmaKindNestedTypeIdentifier
	ecmaKindGenericType
	ecmaKindPredefinedType
	ecmaKindIdentifier
	ecmaKindPropertyIdentifier
	ecmaKindFunctionDeclaration
	ecmaKindFunctionExpression
	ecmaKindArrowFunction
	ecmaKindFormalParameters
	ecmaKindAssignmentExpression
	ecmaKindArguments
	ecmaKindReturnStatement
)

// tsKindNames maps every typescript node-kind spelling the ECMAScript arms name
// onto its class code.
//
// EVERY NAME HERE WAS ENUMERATED AGAINST THE VENDORED GRAMMAR, not read off a
// grammar reference, because newSymbolClasses panics for a name the grammar
// declares no REGULAR symbol under. Two classes of name are excluded by that
// rule and the exclusions are the interesting part of this map:
//
//   - THE HERITAGE KEYWORDS ARE ANONYMOUS. `implements` and `extends` carry no
//     named symbol in this grammar, so neither may appear here. A heritage
//     clause is reached through its REGULAR wrapper instead — implements_clause,
//     extends_clause, extends_type_clause, class_heritage — which is why all
//     four wrappers are named and neither keyword is.
//   - `readonly` IS LIKEWISE ANONYMOUS on a parameter property, which is why the
//     parameter arms locate a parameter's name by KIND rather than by position.
//
// required_parameter and optional_parameter each carry TWO regular symbol ids in
// this grammar. That multiplicity is the reason newSymbolClasses assigns every
// member of a name's symbol set rather than stopping at the first: a table built
// the other way would classify the second id as Other and silently drop half the
// parameters it was written to bind.
var tsKindNames = map[string]uint8{
	"class_declaration":          ecmaKindClassDeclaration,
	"abstract_class_declaration": ecmaKindAbstractClassDeclaration,
	"class":                      ecmaKindClassExpression,
	"class_body":                 ecmaKindClassBody,
	"class_heritage":             ecmaKindClassHeritage,
	"implements_clause":          ecmaKindImplementsClause,
	"extends_clause":             ecmaKindExtendsClause,
	"extends_type_clause":        ecmaKindExtendsTypeClause,
	"interface_declaration":      ecmaKindInterfaceDeclaration,
	"interface_body":             ecmaKindInterfaceBody,
	"type_alias_declaration":     ecmaKindTypeAliasDeclaration,
	"method_signature":           ecmaKindMethodSignature,
	"property_signature":         ecmaKindPropertySignature,
	"method_definition":          ecmaKindMethodDefinition,
	"public_field_definition":    ecmaKindPublicFieldDefinition,
	"type_annotation":            ecmaKindTypeAnnotation,
	"required_parameter":         ecmaKindRequiredParameter,
	"optional_parameter":         ecmaKindOptionalParameter,
	"accessibility_modifier":     ecmaKindAccessibilityModifier,
	"variable_declarator":        ecmaKindVariableDeclarator,
	"new_expression":             ecmaKindNewExpression,
	"call_expression":            ecmaKindCallExpression,
	"member_expression":          ecmaKindMemberExpression,
	"type_identifier":            ecmaKindTypeIdentifier,
	"nested_type_identifier":     ecmaKindNestedTypeIdentifier,
	"generic_type":               ecmaKindGenericType,
	"predefined_type":            ecmaKindPredefinedType,
	"identifier":                 ecmaKindIdentifier,
	"property_identifier":        ecmaKindPropertyIdentifier,
	"function_declaration":       ecmaKindFunctionDeclaration,
	"function_expression":        ecmaKindFunctionExpression,
	"arrow_function":             ecmaKindArrowFunction,
	"formal_parameters":          ecmaKindFormalParameters,
	"assignment_expression":      ecmaKindAssignmentExpression,
	"arguments":                  ecmaKindArguments,
	"return_statement":           ecmaKindReturnStatement,
}

// tsxKindNames is the SAME MAP VALUE, not a copy: the tsx grammar declares every
// kind name above under the same spelling. The tables built from it are still
// separate, because newSymbolClasses enumerates ONE grammar's symbol ids and the
// two grammars number the same names differently — typescript declares 384
// symbols against tsx's 404, so a table built for one classifies the other's
// nodes wrongly rather than not at all.
var tsxKindNames = tsKindNames

// jsKindNames is a SEPARATE, SMALLER map, and the difference is structural
// rather than an omission. Enumerated against the javascript grammar, all of
// abstract_class_declaration, implements_clause, extends_clause,
// extends_type_clause, interface_declaration, interface_body, method_signature,
// property_signature, type_annotation, required_parameter, optional_parameter,
// public_field_definition, type_identifier, nested_type_identifier,
// generic_type, predefined_type and type_alias_declaration declare NO regular
// symbol — the language has no type syntax to declare them for. Naming any one
// of them here would panic at first use, so the two families cannot share a map
// however similar the arms that read them look.
//
// javascript's class heritage differs in shape as well as vocabulary: its
// class_heritage holds the anonymous `extends` token and the supertype
// expression DIRECTLY, with no extends_clause wrapper between them, and a
// javascript class's name is an `identifier` rather than a `type_identifier`.
// Both differences are read by the arms rather than papered over.
var jsKindNames = map[string]uint8{
	"class_declaration":     ecmaKindClassDeclaration,
	"class":                 ecmaKindClassExpression,
	"class_body":            ecmaKindClassBody,
	"class_heritage":        ecmaKindClassHeritage,
	"method_definition":     ecmaKindMethodDefinition,
	"field_definition":      ecmaKindFieldDefinition,
	"variable_declarator":   ecmaKindVariableDeclarator,
	"new_expression":        ecmaKindNewExpression,
	"call_expression":       ecmaKindCallExpression,
	"member_expression":     ecmaKindMemberExpression,
	"identifier":            ecmaKindIdentifier,
	"property_identifier":   ecmaKindPropertyIdentifier,
	"function_declaration":  ecmaKindFunctionDeclaration,
	"function_expression":   ecmaKindFunctionExpression,
	"arrow_function":        ecmaKindArrowFunction,
	"formal_parameters":     ecmaKindFormalParameters,
	"assignment_expression": ecmaKindAssignmentExpression,
	"arguments":             ecmaKindArguments,
	"return_statement":      ecmaKindReturnStatement,
}

// The three memo instances, on the shared kindTable rather than a per-language
// sync.Once each. kindTable already holds the lazy-build-once-per-process
// contract and inherits every rule newSymbolClasses enforces, so an arm that
// declared its own memo would be re-deriving that contract nineteen times over.
var (
	tsKindTable  = kindTable{lang: LangTypeScript, names: tsKindNames}
	tsxKindTable = kindTable{lang: LangTSX, names: tsxKindNames}
	jsKindTable  = kindTable{lang: LangJavaScript, names: jsKindNames}
)

// tsKinds returns the memoized typescript symbol class table.
func tsKinds() symbolClasses { return tsKindTable.get() }

// tsxKinds returns the memoized tsx symbol class table.
func tsxKinds() symbolClasses { return tsxKindTable.get() }

// jsKinds returns the memoized javascript symbol class table.
func jsKinds() symbolClasses { return jsKindTable.get() }
