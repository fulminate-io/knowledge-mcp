// SPDX-License-Identifier: Apache-2.0

package treesitter

// The kotlin kind classes. kotlinKindOther is the ZERO VALUE and therefore the
// class of every symbol the table does not name.
//
// NO ANONYMOUS TOKEN MAY BE NAMED HERE, and kotlin is the language where that
// bites hardest: `interface` carries NO regular grammar symbol — an interface
// parses as a class_declaration with an anonymous `interface` child — so naming
// it in this map would panic the table at first use rather than at compile
// time. Interface-ness is read with the shared anonymous-child helper instead.
const (
	kotlinKindOther uint8 = iota
	kotlinKindSimpleIdentifier
	kotlinKindTypeIdentifier
	kotlinKindUserType
	kotlinKindTypeArguments
	kotlinKindModifiers
	kotlinKindBindingPatternKind
	kotlinKindParameter
	kotlinKindClassParameter
	kotlinKindVariableDeclaration
	kotlinKindPropertyDeclaration
	kotlinKindPrimaryConstructor
	kotlinKindClassBody
	kotlinKindClassDeclaration
	kotlinKindObjectDeclaration
	kotlinKindFunctionDeclaration
	kotlinKindDelegationSpecifier
	kotlinKindConstructorInvocation
	kotlinKindExplicitDelegation
)

// kotlinKindNames maps every kotlin node-kind spelling the kotlin arms name
// onto its class code.
var kotlinKindNames = map[string]uint8{
	"simple_identifier":      kotlinKindSimpleIdentifier,
	"type_identifier":        kotlinKindTypeIdentifier,
	"user_type":              kotlinKindUserType,
	"type_arguments":         kotlinKindTypeArguments,
	"modifiers":              kotlinKindModifiers,
	"binding_pattern_kind":   kotlinKindBindingPatternKind,
	"parameter":              kotlinKindParameter,
	"class_parameter":        kotlinKindClassParameter,
	"variable_declaration":   kotlinKindVariableDeclaration,
	"property_declaration":   kotlinKindPropertyDeclaration,
	"primary_constructor":    kotlinKindPrimaryConstructor,
	"class_body":             kotlinKindClassBody,
	"class_declaration":      kotlinKindClassDeclaration,
	"object_declaration":     kotlinKindObjectDeclaration,
	"function_declaration":   kotlinKindFunctionDeclaration,
	"delegation_specifier":   kotlinKindDelegationSpecifier,
	"constructor_invocation": kotlinKindConstructorInvocation,
	"explicit_delegation":    kotlinKindExplicitDelegation,
}

// kotlinKindTable memoizes the kotlin class table for the process, on the
// shared lazy-build type rather than a sync.Once of this file's own.
var kotlinKindTable = kindTable{lang: LangKotlin, names: kotlinKindNames}

// kotlinKinds returns the memoized kotlin symbol class table.
func kotlinKinds() symbolClasses { return kotlinKindTable.get() }
