// SPDX-License-Identifier: Apache-2.0

// where_kind_validate.go — compile-time validation of where-tree `kind` leaves
// against the resolved grammar's own node-kind vocabulary.
//
// A kind leaf naming a node kind the grammar does not have can never match
// anything, so the call is answerable before the walk starts. Left unvalidated
// it walks the whole scope and returns the same zero a correct search that
// found nothing returns — and on the replace path that silence certifies a
// migration complete after changing nothing.
//
// PLACEMENT. ParseWhere takes only []byte and has no language, so it cannot do
// this: it does not know the grammar. Validation is therefore a separate entry
// point taking the parsed tree plus the RESOLVED language, called by the tool
// handlers immediately after ParseWhere. Widening ParseWhere's signature would
// reach every caller for a concern only the handlers have.
//
// It is not inside Match either. Match runs once per pattern inside the walk,
// and a refusal that is decidable at compile time reported as a walk error
// would conflate two different failure classes.

package ast

import (
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// kindVocabulary is one grammar's symbol table split the way a kind leaf sees
// it: named kinds a leaf can match, and the anonymous tokens it never can.
// Built ONCE per call and passed down, rather than re-enumerated per leaf.
type kindVocabulary struct {
	// named is the set form of sorted, for O(1) membership on the hot check.
	named map[string]struct{}
	// sorted is the same set ordered, for deterministic suggestion scans.
	sorted []string
	// anonymous holds the grammar's literal tokens ('+', '{', 'func'). They are
	// carried because a caller naming one is not making a spelling mistake and
	// deserves a different message, not because a leaf could ever match them.
	anonymous map[string]struct{}
}

// newKindVocabulary enumerates lang's grammar symbol table. This is THE
// enumeration behind both operation=list_node_kinds and kind validation — a
// second one that drifted would recreate the trap where a name the tool prints
// is a name the validator rejects. Returns false when the language has no
// registered grammar; naming that is the caller's own language check to do.
//
// API verification: smacker exposes SymbolCount() uint32, SymbolName(Symbol)
// string and SymbolType(Symbol) SymbolType on *sitter.Language;
// bindings.go:362,372 at $GOMODCACHE/github.com/smacker/go-tree-sitter@<sha>.
func newKindVocabulary(lang treesitter.Language) (*kindVocabulary, bool) {
	grammar, ok := treesitter.LanguageGrammar(lang)
	if !ok || grammar == nil {
		return nil, false
	}
	count := int(grammar.SymbolCount())
	v := &kindVocabulary{
		named:     make(map[string]struct{}, count),
		sorted:    make([]string, 0, count),
		anonymous: make(map[string]struct{}, count),
	}
	for i := range count {
		s := sitter.Symbol(uint16(i))
		name := grammar.SymbolName(s)
		if name == "" {
			continue
		}
		if grammar.SymbolType(s) != sitter.SymbolTypeRegular {
			v.anonymous[name] = struct{}{}
			continue
		}
		if _, dup := v.named[name]; dup {
			continue
		}
		v.named[name] = struct{}{}
		v.sorted = append(v.sorted, name)
	}
	sort.Strings(v.sorted)
	return v, true
}

// NodeKinds returns lang's named node-kind vocabulary, sorted and deduped:
// every symbol the grammar declares regular, which is exactly the set a
// where-tree `kind` leaf can ever match and exactly what operation=
// list_node_kinds prints. Anonymous tokens are excluded from both, because a
// named node's Type() never reports one.
//
// Returns false when the language has no registered grammar.
func NodeKinds(lang treesitter.Language) ([]string, bool) {
	v, ok := newKindVocabulary(lang)
	if !ok {
		return nil, false
	}
	return v.sorted, true
}

// ValidateWhereKinds rejects any `kind` leaf in the where-tree naming a node
// kind lang's grammar does not have, recursing through the composers and
// through inside_pattern / contains_pattern sub-trees — a kind leaf nested in a
// sub-pattern is exactly as undecidable during the walk as one at the top.
//
// It validates the name against the GRAMMAR and never against the corpus. A
// valid kind that simply does not occur in the scope must stay a clean zero; an
// implementation that errored there would break legitimate searches, which is a
// worse defect than the silence this one removes.
//
// A nil tree is no filter, and an unregistered language is not this function's
// error to report — both return nil, leaving the caller's own language check to
// own that message.
func ValidateWhereKinds(where *WhereNode, lang treesitter.Language) error {
	if where == nil {
		return nil
	}
	vocab, ok := newKindVocabulary(lang)
	if !ok {
		return nil
	}
	return validateKindLeaves(where, lang, vocab)
}

// validateKindLeaves is the recursive half of ValidateWhereKinds, carrying the
// once-built vocabulary down the tree.
func validateKindLeaves(where *WhereNode, lang treesitter.Language, vocab *kindVocabulary) error {
	if where == nil {
		return nil
	}
	for _, child := range where.All {
		if err := validateKindLeaves(child, lang, vocab); err != nil {
			return err
		}
	}
	for _, child := range where.Any {
		if err := validateKindLeaves(child, lang, vocab); err != nil {
			return err
		}
	}
	if err := validateKindLeaves(where.Not, lang, vocab); err != nil {
		return err
	}
	if where.Kind != nil {
		for _, name := range where.Kind.Is {
			if err := vocab.checkKind(name, lang); err != nil {
				return err
			}
		}
	}
	for _, sub := range []*SubPatternLeaf{where.InsidePattern, where.ContainsPattern} {
		if sub == nil {
			continue
		}
		if err := validateKindLeaves(sub.Where, lang, vocab); err != nil {
			return err
		}
	}
	return nil
}

// checkKind accepts a name the grammar declares as a named kind and rejects
// everything else, naming the offending kind, the language, and the closest
// valid spellings.
//
// The anonymous-token arm is how callers actually reach this rejection:
// operation=explain prints anonymous tokens that operation=list_node_kinds
// excludes, so copying a name out of an explain tree into a kind leaf lands
// here with a name that is spelled perfectly. Telling that caller they have a
// typo would send them looking for a mistake they did not make.
func (v *kindVocabulary) checkKind(name string, lang treesitter.Language) error {
	if _, ok := v.named[name]; ok {
		return nil
	}
	if _, anon := v.anonymous[name]; anon {
		return fmt.Errorf(
			"ast/where: %q is an anonymous token of the %s grammar, not a named node kind — a kind leaf can only match named kinds. operation=explain prints anonymous tokens (punctuation and keywords) that a kind leaf can never match; operation=list_node_kinds lists the ones it can",
			name, lang)
	}
	suggestions := ClosestVocabulary(name, v.sorted)
	if len(suggestions) == 0 {
		return fmt.Errorf(
			"ast/where: unknown node kind %q for language %s, and no kind in that grammar is close enough to suggest — list the valid kinds with operation=list_node_kinds",
			name, lang)
	}
	quoted := make([]string, 0, len(suggestions))
	for _, s := range suggestions {
		quoted = append(quoted, fmt.Sprintf("%q", s))
	}
	return fmt.Errorf(
		"ast/where: unknown node kind %q for language %s — did you mean %s? A kind leaf naming a kind the grammar lacks can never match, so the call is refused rather than walked. Full vocabulary: operation=list_node_kinds",
		name, lang, strings.Join(quoted, ", "))
}
