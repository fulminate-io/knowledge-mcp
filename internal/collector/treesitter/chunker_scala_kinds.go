// SPDX-License-Identifier: Apache-2.0

package treesitter

// The scala kind classes. scalaKindOther is the ZERO VALUE and therefore the
// class of every symbol the table does not name.
//
// NO ANONYMOUS TOKEN MAY BE NAMED HERE. Scala's `extends` and `with` carry NO
// regular grammar symbol, so naming either would panic the table at first use;
// their POSITION is read by walking the clause's children in order instead,
// which is what lets an extends target and a with-mixin be told apart.
const (
	scalaKindOther uint8 = iota
	scalaKindIdentifier
	scalaKindTypeIdentifier
	scalaKindStableTypeIdentifier
	scalaKindGenericType
	scalaKindTypeArguments
	scalaKindModifiers
	scalaKindAccessModifier
	scalaKindAnnotation
	scalaKindParameter
	scalaKindClassParameter
	scalaKindClassParameters
	scalaKindValDefinition
	scalaKindVarDefinition
	scalaKindValDeclaration
	scalaKindVarDeclaration
	scalaKindTemplateBody
	scalaKindExtendsClause
	scalaKindClassDefinition
	scalaKindObjectDefinition
	scalaKindTraitDefinition
	scalaKindFunctionDefinition
	scalaKindFunctionDeclaration
)

// scalaKindNames maps every scala node-kind spelling the scala arms name onto
// its class code.
var scalaKindNames = map[string]uint8{
	"identifier":             scalaKindIdentifier,
	"type_identifier":        scalaKindTypeIdentifier,
	"stable_type_identifier": scalaKindStableTypeIdentifier,
	"generic_type":           scalaKindGenericType,
	"type_arguments":         scalaKindTypeArguments,
	"modifiers":              scalaKindModifiers,
	"access_modifier":        scalaKindAccessModifier,
	"annotation":             scalaKindAnnotation,
	"parameter":              scalaKindParameter,
	"class_parameter":        scalaKindClassParameter,
	"class_parameters":       scalaKindClassParameters,
	"val_definition":         scalaKindValDefinition,
	"var_definition":         scalaKindVarDefinition,
	"val_declaration":        scalaKindValDeclaration,
	"var_declaration":        scalaKindVarDeclaration,
	"template_body":          scalaKindTemplateBody,
	"extends_clause":         scalaKindExtendsClause,
	"class_definition":       scalaKindClassDefinition,
	"object_definition":      scalaKindObjectDefinition,
	"trait_definition":       scalaKindTraitDefinition,
	"function_definition":    scalaKindFunctionDefinition,
	"function_declaration":   scalaKindFunctionDeclaration,
}

// scalaKindTable memoizes the scala class table for the process, on the shared
// lazy-build type rather than a sync.Once of this file's own.
var scalaKindTable = kindTable{lang: LangScala, names: scalaKindNames}

// scalaKinds returns the memoized scala symbol class table.
func scalaKinds() symbolClasses { return scalaKindTable.get() }
