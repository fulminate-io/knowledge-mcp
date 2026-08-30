// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"fmt"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

// The Go kind classes. goKindOther is the ZERO VALUE and therefore the class of
// every symbol the table does not name, which is what makes an unclassified
// symbol behave like a kind no arm matches rather than like a wrong one.
//
// Every constant below names a kind one of the THREE Go arm files SPELLS — the
// qualifier arm, the type-facts arm, and the flow-step arm — and the table now
// covers that vocabulary WHOLE. There is no longer a fenced-off remainder.
//
// WHY IT IS WORTH CLASSIFYING BY SYMBOL. Node.Type() is
// C.GoString(C.ts_node_type) in the vendored binding, so every kind comparison
// spelled as a string allocates a fresh Go string per node visited. That is
// paid worst by the two RECURSIVE DESCENTS — findNodeByKind and
// findTypeIdentifier — which walk a whole subtree asking one question per node.
// Classifying on Symbol() is a dense []uint8 index instead.
//
// THE FENCE THAT USED TO LIVE HERE IS GONE, and the reason it existed is worth
// keeping. findNodeByType (now findNodeByKind), goSoleTypeSpec and
// extractGoInterfaceEmbeds all reach Chunker.emitDeclarationEdges through
// goAllEmbeds, so they run whether the type-facts arm is registered or not.
// While the standing allocation budget was a RATIO of the armed chunker's cost
// to the un-armed one's, converting them removed real allocations from BOTH
// terms — which lowered the denominator and RAISED the ratio, so the instrument
// scored a genuine improvement as a regression. They were left unconverted for
// that reason alone. The budget is now an ABSOLUTE per-leg allocation ceiling,
// which has no such perversity, and the conversion was taken. Nothing about the
// code changed the answer; the instrument did.
//
// CITED BY SYMBOL, NOT BY LINE, DELIBERATELY. An earlier form of this comment
// carried line numbers into chunker_go_typefacts.go, and the same commit that
// wrote it inserted lines above those calls and moved them — a citation into a
// file the citing change also edits rots before the change even lands.
const (
	goKindOther uint8 = iota
	goKindFuncLiteral
	goKindVarDecl
	goKindConstDecl
	goKindShortVarDecl
	goKindParameterList
	goKindBlock
	goKindParameterDeclaration
	goKindIdentifier
	goKindExpressionList
	goKindVarSpec
	goKindConstSpec
	goKindTypeIdentifier
	goKindQualifiedType
	goKindGenericType
	goKindPointerType
	goKindParenthesizedType
	goKindCompositeLiteral
	goKindUnaryExpression
	goKindTypeAssertionExpression
	goKindCallExpression
	goKindSelectorExpression
	goKindFunctionDeclaration
	goKindMethodDeclaration
	goKindFieldIdentifier
	goKindStructType
	goKindFieldDeclarationList
	goKindFieldDeclaration
	goKindInterpretedStringLiteral
	goKindRawStringLiteral
	goKindSliceType
	goKindArrayType
	goKindChannelType
	goKindMapType
	goKindFunctionType
	goKindInterfaceType
	goKindTypeArguments
	goKindTypeElem
	goKindVariadicParameterDeclaration
	goKindMethodElem
	goKindTypeParameterList
	goKindTypeSpec
	// The flow-step arm's vocabulary. goKindVariadicParameterDeclaration is
	// named by the signature composition above AND by the flow arm, and is
	// declared ONCE: a second constant would be a second class code for one
	// grammar spelling, and newSymbolClasses assigns a spelling to exactly one.
	goKindAssignmentStatement
	goKindReturnStatement
	goKindArgumentList
)

// goKindNames maps every Go node-kind spelling the three arm files name onto
// its class code. It is the input to newSymbolClasses and the enumeration
// TestSymbolClassesCoverEveryGrammarSpelling walks, so a kind added to an arm
// without an entry here classifies as goKindOther and is caught by the arm's
// own behavior gates rather than silently mis-binding.
var goKindNames = map[string]uint8{
	"func_literal":               goKindFuncLiteral,
	"var_declaration":            goKindVarDecl,
	"const_declaration":          goKindConstDecl,
	"short_var_declaration":      goKindShortVarDecl,
	"parameter_list":             goKindParameterList,
	"block":                      goKindBlock,
	"parameter_declaration":      goKindParameterDeclaration,
	"identifier":                 goKindIdentifier,
	"expression_list":            goKindExpressionList,
	"var_spec":                   goKindVarSpec,
	"const_spec":                 goKindConstSpec,
	"type_identifier":            goKindTypeIdentifier,
	"qualified_type":             goKindQualifiedType,
	"generic_type":               goKindGenericType,
	"pointer_type":               goKindPointerType,
	"parenthesized_type":         goKindParenthesizedType,
	"composite_literal":          goKindCompositeLiteral,
	"unary_expression":           goKindUnaryExpression,
	"type_assertion_expression":  goKindTypeAssertionExpression,
	"call_expression":            goKindCallExpression,
	"selector_expression":        goKindSelectorExpression,
	"function_declaration":       goKindFunctionDeclaration,
	"method_declaration":         goKindMethodDeclaration,
	"field_identifier":           goKindFieldIdentifier,
	"struct_type":                goKindStructType,
	"field_declaration_list":     goKindFieldDeclarationList,
	"field_declaration":          goKindFieldDeclaration,
	"interpreted_string_literal": goKindInterpretedStringLiteral,
	"raw_string_literal":         goKindRawStringLiteral,
	// The signature-composition vocabulary. Every name below was checked
	// against the vendored Go grammar's declared node kinds before being added,
	// because newSymbolClasses PANICS at first use for a name the grammar
	// declares no regular symbol under.
	"slice_type":                     goKindSliceType,
	"array_type":                     goKindArrayType,
	"channel_type":                   goKindChannelType,
	"map_type":                       goKindMapType,
	"function_type":                  goKindFunctionType,
	"interface_type":                 goKindInterfaceType,
	"type_arguments":                 goKindTypeArguments,
	"type_elem":                      goKindTypeElem,
	"variadic_parameter_declaration": goKindVariadicParameterDeclaration,
	"method_elem":                    goKindMethodElem,
	"type_parameter_list":            goKindTypeParameterList,
	"type_spec":                      goKindTypeSpec,
	// The flow-step vocabulary.
	"assignment_statement": goKindAssignmentStatement,
	"return_statement":     goKindReturnStatement,
	"argument_list":        goKindArgumentList,
}

// symbolClasses classifies a grammar's symbol ids by class code: one dense
// []uint8 indexed by the numeric symbol a parsed node reports, so classifying a
// node is one bounds-checked array index instead of a cgo call returning a
// freshly allocated C string.
type symbolClasses []uint8

// class returns the class of one symbol, or goKindOther for any symbol the
// table does not cover.
//
// THE BOUNDS CHECK IS LOAD-BEARING AND NOT DEFENSIVE PADDING. An ERROR node is
// a NAMED child — IsNamed() reports true and a walk over named children visits
// it — and its Symbol() is 65535 while the Go grammar's SymbolCount() is 217.
// The collector meets error-tolerant parses routinely, and an ERROR sits INSIDE
// the declaration subtree the qualifier walk descends, so an unchecked index
// here is a panic in the chunker on ordinary malformed input rather than an
// exotic edge case. MISSING nodes need no such care: a MISSING identifier
// carries symbol 1, in range.
func (c symbolClasses) class(s sitter.Symbol) uint8 {
	if int(s) >= len(c) {
		return goKindOther
	}
	return c[s]
}

// newSymbolClasses derives one language's class table by enumerating its
// grammar's symbol ids.
//
// A KIND NAME MAPS TO A SET OF SYMBOLS, NEVER TO ONE, and every member of that
// set is assigned. The Go grammar declares 217 symbols but only 104 distinct
// regular names: "identifier" is symbols 1, 60 and 61, "argument_list" is 172
// and 173, "labeled_statement" is 147 and 148. A builder that stopped at the
// first match would leave 60 and 61 classified as goKindOther — a latent wrong
// answer that today's corpus happens not to surface, which is exactly why
// TestSymbolClassesCoverEveryGrammarSpelling asserts the whole set rather than
// trusting a runtime symptom to appear.
//
// The forward name-to-id direction is DERIVED rather than looked up: the
// vendored binding at this pin exposes no ts_language_symbol_for_name, so the
// only route is one pass over SymbolCount() reading SymbolName. The enumeration
// mirrors newKindVocabulary (cmd/knowledge/internal/ast/where_kind_validate.go)
// with its dedupe removed, because the multiplicity that function discards is
// precisely the information this table depends on. That function is not
// delegated to for a second reason as well: package ast imports this package,
// so the dependency cannot run the other way.
//
// IT RETURNS A NON-EMPTY TABLE OR PANICS, AND NEVER RETURNS NIL. An empty table
// would classify every symbol as goKindOther, goKinds' sync.Once would memoize
// that answer for the life of the process, and the arm would bind nothing at
// all while every unit test building its own fixtures still passed. An
// unregistered grammar, a zero SymbolCount and a kind name the grammar does not
// declare are each a programming error, and each is reported as one.
func newSymbolClasses(lang Language, byKind map[string]uint8) symbolClasses {
	grammar, ok := LanguageGrammar(lang)
	if !ok || grammar == nil {
		panic(fmt.Sprintf("treesitter: newSymbolClasses(%s): no grammar is registered for this language", lang))
	}
	count := int(grammar.SymbolCount())
	if count == 0 {
		panic(fmt.Sprintf("treesitter: newSymbolClasses(%s): the grammar declares zero symbols", lang))
	}

	table := make(symbolClasses, count)
	assigned := make(map[string]int, len(byKind))
	for i := range count {
		s := sitter.Symbol(uint16(i)) //nolint:gosec // i is bounded by SymbolCount, which the grammar declares as a uint16-addressable symbol table
		if grammar.SymbolType(s) != sitter.SymbolTypeRegular {
			continue
		}
		name := grammar.SymbolName(s)
		code, wanted := byKind[name]
		if !wanted {
			continue
		}
		table[i] = code
		assigned[name]++
	}

	for name := range byKind {
		if assigned[name] == 0 {
			panic(fmt.Sprintf("treesitter: newSymbolClasses(%s): the grammar declares no regular symbol named %q, so the class map names a kind that cannot exist", lang, name))
		}
	}
	return table
}

// kindTable memoizes ONE language's symbol class table for the process.
//
// The lazy shape imitates langEntry.Queries (language.go): built on first use,
// cached, thread-safe. It is deliberately NOT built in init() — the per-language
// follow-up work adds up to nineteen more of these, and a process that chunks
// only Go should not pay for the rest. The memoization is also the second
// reason newSymbolClasses panics rather than returning a degraded table: a bad
// table cached here would poison every later call in the process.
//
// IT DECIDES WHEN newSymbolClasses RUNS, NEVER WHAT IT ACCEPTS, so every rule
// that function enforces is inherited unchanged — and one of them constrains
// every arm that ever declares a table here. newSymbolClasses walks only
// REGULAR symbols and then panics on any kinds-map name that received zero
// assignments, so A KINDS MAP NAMING AN ANONYMOUS TOKEN PANICS AT FIRST USE. A
// language whose keyword carries no named symbol therefore cannot be asked
// about through this table at all; a consumer that needs to know whether such a
// keyword is PRESENT uses hasAnonymousChild (chunker_imports_dotted.go), which
// walks a node's children comparing .Type(). That is the presence reuse target;
// the class table is not.
type kindTable struct {
	lang  Language
	names map[string]uint8
	once  sync.Once
	table symbolClasses
}

// get returns the memoized table, building it on the first call.
func (t *kindTable) get() symbolClasses {
	t.once.Do(func() {
		t.table = newSymbolClasses(t.lang, t.names)
	})
	return t.table
}

// goKindTable is the Go instance, and the only one this package registers.
var goKindTable = kindTable{lang: LangGo, names: goKindNames}

// goKinds returns the memoized Go symbol class table.
func goKinds() symbolClasses {
	return goKindTable.get()
}
