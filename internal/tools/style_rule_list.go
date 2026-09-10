// SPDX-License-Identifier: Apache-2.0

// style_rule_list.go — the generic rule-list FILE FORMAT and its parser.
//
// WHY A GENERIC FORMAT RATHER THAN A LINTER ADAPTER. A style rule is whatever
// the author requires, and the sources are a hand-written list, a harvested
// style guide and, later, a linter's own configuration. A generic list is the
// one shape all three can be produced into, and it is the shape an LLM can write
// directly; per-linter adapters are collectors that emit this, later.
//
// EVERY REFUSAL NAMES THE RULE'S ARRAY INDEX IN THE CALLER'S FILE. A refusal
// that names only the field sends an author looking through a list for which
// entry it meant. Nothing here coerces, defaults or drops: a rule this parser
// cannot represent is a rule the author believes was imported, so it is refused.
//
// JSON RATHER THAN YAML, measured rather than preferred: nothing in the client's
// tool, corpus, engine or corpus-scan packages unmarshals YAML, so YAML would add
// a dependency for a format whose principal author is an LLM already writing JSON.

package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/validate"
)

// styleRuleList is the document's top level: one `rules` array and nothing else.
type styleRuleList struct {
	Rules []json.RawMessage `json:"rules"`
}

// styleRuleEntry is one rule as the file spells it. The three optional blocks are
// POINTERS so an absent block and a present-but-empty one stay different things:
// an absent `scope` means the rule applies everywhere, and a `"scope": {}` is an
// author saying something they probably did not mean, which the validator
// refuses rather than reading as the default.
type styleRuleEntry struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	Summary  string               `json:"summary"`
	Text     string               `json:"text"`
	Severity string               `json:"severity"`
	Scope    *styleRuleEntryScope `json:"scope"`
	Linter   *styleRuleEntryLint  `json:"linter"`
	Check    *styleRuleEntryCheck `json:"check"`
}

// styleRuleEntryScope is the optional repo/path narrowing.
type styleRuleEntryScope struct {
	Repo  string   `json:"repo"`
	Paths []string `json:"paths"`
}

// styleRuleEntryLint is the optional linter provenance, as TWO fields. A single
// packed "golangci:modernize" would force a parse on a separator a linter rule id
// may itself contain.
type styleRuleEntryLint struct {
	Name   string `json:"name"`
	RuleID string `json:"rule_id"`
}

// styleRuleEntryCheck is the optional SHAPE a rule carries for the later
// check-authoring step.
//
// IT IS INERT DATA AND THIS IMPORT NEVER COMPILES IT, never creates a check, and
// never creates a fixture node. The corpus-check gate returns before doing
// anything unless the write targets the checks graph, so a pattern riding a
// practice node is never executed by anything in the tree. The check itself is
// authored by its own tool call, after an LLM has passed on the practice node —
// the owner's ruling, and the reason nothing here calls manage_checks.
type styleRuleEntryCheck struct {
	DSLPattern  string `json:"dsl_pattern"`
	CheckWhere  string `json:"check_where"`
	FixtureBad  string `json:"fixture_bad"`
	FixtureGood string `json:"fixture_good"`
}

// parseStyleRuleList decodes and validates a whole rule-list document.
//
// IT VALIDATES EVERY RULE BEFORE ANY OF THEM IS WRITTEN, which is what makes an
// invalid rule 3 of 5 leave zero nodes behind rather than two. The first refusal
// returns, naming the rule.
func parseStyleRuleList(body []byte) ([]styleRuleEntry, error) {
	var doc styleRuleList
	if err := strictUnmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("rule list: %w", err)
	}
	if len(doc.Rules) == 0 {
		return nil, fmt.Errorf("rule list: `rules` is empty — there is nothing to import")
	}
	out := make([]styleRuleEntry, 0, len(doc.Rules))
	seen := map[string]int{}
	for i, raw := range doc.Rules {
		entry, err := parseStyleRuleEntry(i, raw)
		if err != nil {
			return nil, err
		}
		if entry.ID != "" {
			if first, dup := seen[entry.ID]; dup {
				return nil, fmt.Errorf(
					"rule list: rules[%d] and rules[%d] both carry id %q — "+
						"one id names one rule, and this import will not adjudicate which of the two wins",
					first, i, entry.ID)
			}
			seen[entry.ID] = i
		}
		out = append(out, entry)
	}
	return out, nil
}

// parseStyleRuleEntry decodes and validates ONE rule.
func parseStyleRuleEntry(i int, raw json.RawMessage) (styleRuleEntry, error) {
	var e styleRuleEntry
	if err := strictUnmarshal(raw, &e); err != nil {
		return styleRuleEntry{}, fmt.Errorf("rule list: rules[%d]: %w", i, err)
	}
	if err := validateStyleRuleEntry(i, e); err != nil {
		return styleRuleEntry{}, err
	}
	return e, nil
}

// validateStyleRuleEntry is every refusal one rule can earn.
//
// THE NAME AND SUMMARY REFUSALS ARE HERE EVEN THOUGH THE SERVER REFUSES THEM
// TOO. validateCreateNodeBody already rejects a summary-less, name-less or
// over-length `pattern` body and names the batch index, and this import writes
// no validator of its own for what those two fields must CONTAIN. What it adds
// is the LOCATOR: a refusal naming the position in the author's own file is the
// one they can act on, and it arrives before the batch is composed rather than
// after it is rejected.
//
// THE LENGTH CAP IS NOT RE-DECLARED HERE. validate.Summary carries
// validate.SummaryMaxLen, which is the CLIENT TWIN of the server's own
// summaryMaxLen — the two modules cannot import each other, so the value is
// mirrored in exactly those two places and a third literal in this file would be
// a fourth 500 to keep in step. It counts RUNES, as the server's does, so a
// 500-rune summary of multi-byte runes is admitted rather than refused on bytes.
func validateStyleRuleEntry(i int, e styleRuleEntry) error {
	where := fmt.Sprintf("rule list: rules[%d]", i)
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("%s has no `name` — a style rule is addressed by name and the server refuses a nameless pattern body", where)
	}
	if strings.TrimSpace(e.Summary) == "" {
		return fmt.Errorf(
			"%s (%q) has no `summary` — the one-line summary is what makes the rule findable, "+
				"it is author-supplied and nothing composes one for you", where, e.Name)
	}
	if err := validate.Summary("rule list", fmt.Sprintf("rules[%d] (%q) `summary`", i, e.Name), e.Summary); err != nil {
		return err
	}
	if strings.TrimSpace(e.Text) == "" {
		return fmt.Errorf("%s (%q) has no `text` — the rule's own statement is its body", where, e.Name)
	}
	if _, err := styleRuleSeverity(fmt.Sprintf("%s (%q)", where, e.Name), e.Severity); err != nil {
		return err
	}
	if err := validateStyleRuleScope(where, e); err != nil {
		return err
	}
	return validateStyleRuleLinter(where, e)
}

// validateStyleRuleScope refuses an empty scope block and every path spelling
// the walk could never match.
func validateStyleRuleScope(where string, e styleRuleEntry) error {
	if e.Scope == nil {
		return nil
	}
	if e.Scope.Repo == "" && len(e.Scope.Paths) == 0 {
		return fmt.Errorf(
			"%s (%q) carries an empty `scope` block — omit `scope` entirely to mean "+
				"\"applies to every repo and every path\", which is what an absent scope means",
			where, e.Name)
	}
	for j, p := range e.Scope.Paths {
		if refusal := styleScopePathRefusal(p); refusal != "" {
			return fmt.Errorf("%s (%q): scope.paths[%d] %q %s", where, e.Name, j, p, refusal)
		}
	}
	return nil
}

// validateStyleRuleLinter refuses a linter block that names neither half. Either
// half alone is legitimate — a rule may record the linter without a rule id —
// but a block carrying nothing records nothing.
func validateStyleRuleLinter(where string, e styleRuleEntry) error {
	if e.Linter == nil {
		return nil
	}
	if e.Linter.Name == "" && e.Linter.RuleID == "" {
		return fmt.Errorf(
			"%s (%q) carries an empty `linter` block — omit `linter` entirely when the rule has no linter provenance",
			where, e.Name)
	}
	return nil
}

// styleRuleMetadata renders one parsed rule as the practice node's metadata.
//
// AN OPTIONAL KEY IS ABSENT RATHER THAN EMPTY, which is checkNodeMetadata's own
// rule and the reason it is worth restating: the readers below treat a missing
// key as the default, and writing an empty value would invent a second spelling
// of that default which every one of them then has to know about.
//
// NO sister_check VALUE IS EVER WRITTEN HERE. The cross-link is filled by
// whoever authors the sister check, in its own call, after an LLM pass — this
// import creates no check and links to none.
func styleRuleMetadata(e styleRuleEntry) (map[string]string, error) {
	md := map[string]string{
		kgtypes.MetaKeyPracticeKind: kgtypes.PracticeKindStyleRule,
		"severity":                  e.Severity,
	}
	if err := addStyleRuleScope(md, e.Scope); err != nil {
		return nil, err
	}
	if e.Linter != nil {
		if e.Linter.Name != "" {
			md[kgtypes.MetaKeyLinterName] = e.Linter.Name
		}
		if e.Linter.RuleID != "" {
			md[kgtypes.MetaKeyLinterRuleID] = e.Linter.RuleID
		}
	}
	addStyleRuleCheckShape(md, e.Check)
	return md, nil
}

// addStyleRuleScope folds a rule's optional scope onto its metadata. Each half
// is written only when the author supplied it, so an absent scope leaves both
// keys ABSENT rather than empty.
func addStyleRuleScope(md map[string]string, sc *styleRuleEntryScope) error {
	if sc == nil {
		return nil
	}
	if sc.Repo != "" {
		md[kgtypes.MetaKeyStyleScopeRepo] = sc.Repo
	}
	if len(sc.Paths) == 0 {
		return nil
	}
	encoded, err := styleScopePathsEncode(sc.Paths)
	if err != nil {
		return err
	}
	md[kgtypes.MetaKeyStyleScopePaths] = encoded
	return nil
}

// addStyleRuleCheckShape folds a rule's optional check shape onto its metadata
// as INERT DATA for the later check-authoring step. Nothing compiles it and this
// import creates no check.
func addStyleRuleCheckShape(md map[string]string, c *styleRuleEntryCheck) {
	if c == nil {
		return
	}
	for key, value := range map[string]string{
		"dsl_pattern":        c.DSLPattern,
		"check_where":        c.CheckWhere,
		"check_fixture_bad":  c.FixtureBad,
		"check_fixture_good": c.FixtureGood,
	} {
		if value != "" {
			md[key] = value
		}
	}
}

// strictUnmarshal decodes with DisallowUnknownFields, so a key the format does
// not define is REFUSED naming the key rather than dropped.
//
// A DROPPED KEY IS THE FAILURE THIS EXISTS TO PREVENT: an author who misspells
// `severity` gets a rule imported without one, and nothing in the response says
// so. The decoder's own message names the offending field, which is the actionable
// half.
func strictUnmarshal(body []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return err
	}
	// A second Decode returning anything but io.EOF means the document carried
	// trailing content past the object — a second JSON value in one file is a
	// file the author believes was imported whole.
	if dec.More() {
		return fmt.Errorf("trailing content after the JSON document")
	}
	return nil
}
