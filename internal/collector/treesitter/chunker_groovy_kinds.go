// SPDX-License-Identifier: Apache-2.0

package treesitter

// The groovy kind classes. groovyKindOther is the ZERO VALUE and therefore the
// class of every symbol the table does not name.
//
// NO ANONYMOUS TOKEN MAY BE NAMED HERE. Groovy's `extends` and `interface` each
// carry NO regular grammar symbol, so naming either would panic the table at
// first use; presence is read with the shared anonymous-child helper and
// POSITION by walking the declaration's children in order.
//
// A KIND NAME MAPS TO A SET OF SYMBOLS, NEVER TO ONE: this grammar declares
// `identifier` at more than one regular symbol id, and the table builder
// assigns every symbol matching a name rather than the first.
const (
	groovyKindOther uint8 = iota
	groovyKindIdentifier
	groovyKindDottedIdentifier
	groovyKindBuiltinType
	groovyKindModifier
	groovyKindAccessModifier
	groovyKindAnnotation
	groovyKindParameter
	groovyKindParameterList
	groovyKindDeclaration
	groovyKindClosure
	groovyKindClassDefinition
	groovyKindFunctionDefinition
	groovyKindFunctionDeclaration
	groovyKindFunctionCall
	groovyKindJuxtFunctionCall
	groovyKindArgumentList
	groovyKindAssignment
	groovyKindReturn
)

// groovyKindNames maps every groovy node-kind spelling the groovy arms name
// onto its class code.
var groovyKindNames = map[string]uint8{
	"identifier":           groovyKindIdentifier,
	"dotted_identifier":    groovyKindDottedIdentifier,
	"builtintype":          groovyKindBuiltinType,
	"modifier":             groovyKindModifier,
	"access_modifier":      groovyKindAccessModifier,
	"annotation":           groovyKindAnnotation,
	"parameter":            groovyKindParameter,
	"parameter_list":       groovyKindParameterList,
	"declaration":          groovyKindDeclaration,
	"closure":              groovyKindClosure,
	"class_definition":     groovyKindClassDefinition,
	"function_definition":  groovyKindFunctionDefinition,
	"function_declaration": groovyKindFunctionDeclaration,
	"function_call":        groovyKindFunctionCall,
	"juxt_function_call":   groovyKindJuxtFunctionCall,
	"argument_list":        groovyKindArgumentList,
	"assignment":           groovyKindAssignment,
	"return":               groovyKindReturn,
}

// groovyKindTable memoizes the groovy class table for the process, on the
// shared lazy-build type rather than a sync.Once of this file's own.
var groovyKindTable = kindTable{lang: LangGroovy, names: groovyKindNames}

// groovyKinds returns the memoized groovy symbol class table.
func groovyKinds() symbolClasses { return groovyKindTable.get() }
