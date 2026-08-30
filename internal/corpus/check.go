// SPDX-License-Identifier: Apache-2.0

// Package corpus declares the check contract for checks-graph nodes: the
// metadata vocabulary that turns a finding into an executable CHECK, the Check
// value a consumer executes, and ParseCheck, the single admission parser every
// writer and every executor runs.
//
// A checks-graph finding carries two halves. CHECK IS THE MACHINE HALF ONLY:
// identity is Check.ID, and the prose half lives on the source node — SymbolName,
// Description and Content stay there, ParseCheck never copies them, and a
// consumer that needs a check's title or guidance text reads it from the node it
// already holds rather than expecting Check to carry it.
//
// ParseCheck's return table has exactly four rows, and a consumer that collapses
// any two of them loses information the read surface is required to show:
//
//  1. check_type present and valid → (fully populated Check, true,  nil)
//  2. llm_only == "true" and valid → (Check{ID, LLMOnly: true},    false, nil)
//  3. neither key present          → (zero Check,                  false, nil)
//  4. any rule below violated      → (zero Check,                  false, err)
//
// isCheck TRUE means EXECUTABLE CHECK and nothing else. Row 2 must not collapse
// into row 3: an accepted llm_only node is prose with no deterministic
// expression, which is a REPORTABLE LANE rather than an ordinary node, so a
// consumer's silent-skip branch tests Check.LLMOnly before skipping and scan
// output can honestly split machine-verified from needs-LLM-judgment.
//
// WHAT THIS CONTRACT DOES NOT GOVERN. A node with neither key is not a check
// (row 3) and this contract constrains nothing else about its metadata. Catalog
// content an executor CONSULTS — a source/sink/sanitizer table, a symbol list, a
// threshold table — is data, not an assertion that fires, so it carries no
// check_type and no fixture keys and the admission gate never looks at it.
// There is no fixture-exempt check type: a shape that cannot be silent on a good example is data, not a check.
//
// Every vocabulary is DELEGATED rather than re-spelled. Severity is exactly the
// foundation ladder, language is whatever the tree-sitter registry has a grammar
// for, and an ast_pattern body is whatever the ast engine's own parser and
// compiler accept. Bad input always errors, naming the offending key and the
// admitted vocabulary; nothing is coerced or defaulted.
//
// NOTHING ABOUT A PASSED VALIDATION IS EVER PERSISTED — no timestamp, no digest,
// no stamp. Every executor re-runs the fixtures immediately before executing the
// check, so a fixture edited after admission cannot leave a stale approval
// behind.
//
// corpus is NOT a dependency-free leaf: it imports topology/foundation for the
// severity ladder, and foundation imports the client engine, so the engine is in
// this package's transitive closure. That edge is taken deliberately, for the
// one reason of not re-spelling the severity vocabulary. corpus imports no
// tools, render or recipe package directly — node reads live in the caller —
// which is what lets the scan analyzer and the recipe write fence both import it.
package corpus

import (
	"fmt"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// The authoritative metadata vocabulary. Presence of MetaCheckType is what makes
// a node a check; every other key is read only once that one is present.
const (
	// MetaCheckType names the check's execution kind. Its presence is the
	// discriminant: a node carrying it is a check, a node without it is not.
	MetaCheckType = "check_type"
	// MetaSeverity carries the foundation severity the check's findings are
	// emitted at.
	MetaSeverity = "severity"
	// MetaLanguage names the tree-sitter language the check is written against.
	MetaLanguage = "language"
	// MetaDSLPattern carries the ast pattern DSL body. This key is REUSED from
	// the existing pattern-catalog vocabulary rather than introduced here.
	MetaDSLPattern = "dsl_pattern"
	// MetaCheckWhere carries an optional ast where-tree as JSON text.
	MetaCheckWhere = "check_where"
	// MetaFixtureBad names the node whose Content the check MUST match.
	MetaFixtureBad = "check_fixture_bad"
	// MetaFixtureGood names the node whose Content the check MUST NOT match.
	MetaFixtureGood = "check_fixture_good"
	// MetaLLMOnly marks prose that has no deterministic expression. It is
	// exclusive with every check key: a node claiming both is a coerced check.
	MetaLLMOnly = "llm_only"
)

// CheckType is the closed set of execution kinds a check may declare.
type CheckType string

// The admitted check types. Anything else is refused, not coerced.
const (
	CheckAstPattern        = CheckType("ast_pattern")
	CheckGraphAssertion    = CheckType("graph_assertion")
	CheckTopologyThreshold = CheckType("topology_threshold")
	CheckFlowModel         = CheckType("flow_model")
)

// llmOnlyTrue is the only admitted value of MetaLLMOnly. A key present with any
// other value is an error rather than a false.
const llmOnlyTrue = "true"

// admittedCheckTypes is the closed set, in the order error messages enumerate it.
var admittedCheckTypes = []CheckType{
	CheckAstPattern,
	CheckGraphAssertion,
	CheckTopologyThreshold,
	CheckFlowModel,
}

// admittedSeverities delegates to the foundation ladder rather than re-spelling
// it. The scan analyzer emits a check's severity straight onto a
// foundation.Finding, so a second spelling here would be a second vocabulary
// that drifts from the one that ships.
var admittedSeverities = []foundation.Severity{
	foundation.SeverityInfo,
	foundation.SeverityNotice,
	foundation.SeverityWarning,
	foundation.SeverityCritical,
}

// llmOnlyExcludedKeys are the keys a valid llm_only node must not carry.
var llmOnlyExcludedKeys = []string{
	MetaCheckType,
	MetaDSLPattern,
	MetaCheckWhere,
	MetaFixtureBad,
	MetaFixtureGood,
}

// Check is the machine half of a check-carrying node: everything an
// executor needs to run the check, and nothing a human needs to read it. The
// prose half stays on the source node, reachable through ID.
type Check struct {
	// ID is the SOURCE NODE's id — the check's identity, and what a consumer
	// names when it reports on the check.
	ID string
	// LLMOnly is true only on the accepted-llm_only return (row 2), where it is
	// the sole populated field besides ID.
	LLMOnly bool
	// Type is the validated execution kind.
	Type CheckType
	// Severity is the validated foundation severity.
	Severity foundation.Severity
	// Language is the validated tree-sitter language.
	Language treesitter.Language
	// Pattern is the check's body — for ast_pattern, a DSL pattern proven to
	// parse and compile against Language.
	Pattern string
	// Where is the optional ast where-tree as JSON, nil when absent.
	Where []byte
	// FixtureBad names the node the check must fire on.
	FixtureBad string
	// FixtureGood names the node the check must be silent on.
	FixtureGood string
}

// ParseCheck classifies a node against the check contract, returning the machine
// half, whether the node is an EXECUTABLE check, and any admission error. See
// the package doc for the four-row return table; the rows are load-bearing and
// callers must not collapse them.
func ParseCheck(n *knowledgev1.Node) (Check, bool, error) {
	if n == nil {
		return Check{}, false, nil
	}
	md := n.GetMetadata()
	if raw, ok := md[MetaLLMOnly]; ok {
		c, err := parseLLMOnly(n.GetId(), raw, md)
		if err != nil {
			return Check{}, false, err
		}
		return c, false, nil
	}
	rawType, ok := md[MetaCheckType]
	if !ok {
		return Check{}, false, nil
	}
	c, err := parseCheckBody(n.GetId(), rawType, md)
	if err != nil {
		return Check{}, false, err
	}
	return c, true, nil
}

// parseLLMOnly validates row 2. The exclusivity sweep is the whole point: a node
// carrying both llm_only and a check key is an author coercing prose into a
// check, which is exactly what the marker exists to make unnecessary.
//
// LANGUAGE IS REQUIRED HERE, and it is NOT one of the excluded keys — the
// exclusion set is the CHECK-BODY keys (check_type, dsl_pattern, check_where and
// the two fixtures), never the descriptive ones.
//
// It became required when checks moved into ONE graph. Previously an llm_only
// node sat in a per-language graph, so its language was implied by WHERE it
// lived; now every corpus read narrows by a language metadata predicate, and a
// node without the key matches no predicate and is returned to nobody. That is
// the precise failure the llm_only lane exists to prevent — the contract's own
// warning is that a collapsed lane makes the needs-LLM-judgment population
// invisible and lets a machine-verified result be mistaken for a complete one.
// Silently unreadable is the same outcome by a different route, so this is an
// error at admission rather than a disappearance at scan time.
func parseLLMOnly(id, raw string, md map[string]string) (Check, error) {
	if raw != llmOnlyTrue {
		return Check{}, fmt.Errorf("corpus: %s=%q is not admitted (the only admitted value is %q)", MetaLLMOnly, raw, llmOnlyTrue)
	}
	for _, k := range llmOnlyExcludedKeys {
		if _, present := md[k]; present {
			return Check{}, fmt.Errorf("corpus: a %s node must not also carry %s — prose with no deterministic expression cannot also be an executable check", MetaLLMOnly, k)
		}
	}
	lang, err := parseLanguage(md[MetaLanguage], "")
	if err != nil {
		return Check{}, fmt.Errorf("corpus: a %s node must name the language its guidance is about, "+
			"or no language-scoped scan can ever surface it: %w", MetaLLMOnly, err)
	}
	return Check{ID: id, LLMOnly: true, Language: lang}, nil
}

// parseCheckBody validates row 1: the discriminant, then the value vocabularies,
// then the fixtures, then the per-type body.
func parseCheckBody(id, rawType string, md map[string]string) (Check, error) {
	ct, err := parseCheckType(rawType)
	if err != nil {
		return Check{}, err
	}
	sev, err := parseSeverity(md[MetaSeverity])
	if err != nil {
		return Check{}, err
	}
	lang, err := parseLanguage(md[MetaLanguage], ct)
	if err != nil {
		return Check{}, err
	}
	bad, good, err := parseFixtures(md)
	if err != nil {
		return Check{}, err
	}
	c := Check{
		ID:          id,
		Type:        ct,
		Severity:    sev,
		Language:    lang,
		Pattern:     md[MetaDSLPattern],
		FixtureBad:  bad,
		FixtureGood: good,
	}
	if where := md[MetaCheckWhere]; where != "" {
		c.Where = []byte(where)
	}
	if ct == CheckAstPattern {
		if err := validateAstBody(c); err != nil {
			return Check{}, err
		}
	}
	return c, nil
}

// parseCheckType enforces the closed set and enumerates it on refusal.
func parseCheckType(raw string) (CheckType, error) {
	for _, t := range admittedCheckTypes {
		if raw == string(t) {
			return t, nil
		}
	}
	return "", fmt.Errorf("corpus: %s=%q is not an admitted check type (admitted: %s)", MetaCheckType, raw, checkTypeVocabulary())
}

// parseSeverity resolves the value against the foundation ladder. The admitted
// values are rendered from the constants at call time, so the error message
// cannot drift from what the ladder actually admits.
func parseSeverity(raw string) (foundation.Severity, error) {
	for _, s := range admittedSeverities {
		if raw == string(s) {
			return s, nil
		}
	}
	return "", fmt.Errorf("corpus: %s=%q is not an admitted severity (admitted: %s)", MetaSeverity, raw, severityVocabulary())
}

// parseLanguage requires a registered grammar for every check type, and for
// ast_pattern additionally refuses a language the ast engine denies: a denied
// grammar can never execute a pattern, so admitting one would create a check
// that is silent by construction rather than silent because the code is clean.
func parseLanguage(raw string, ct CheckType) (treesitter.Language, error) {
	lang := treesitter.Language(raw)
	if _, ok := treesitter.LanguageGrammar(lang); !ok {
		return "", fmt.Errorf("corpus: %s=%q has no registered tree-sitter grammar", MetaLanguage, raw)
	}
	if ct == CheckAstPattern && ast.IsDeniedLanguage(lang) {
		return "", fmt.Errorf("corpus: %s=%q is denied by the ast engine, so a %s check written against it could never fire", MetaLanguage, raw, CheckAstPattern)
	}
	return lang, nil
}

// parseFixtures requires both bindings and requires them to differ. They are
// node ids resolved in the check's OWN checks graph, and THE BINDING IS
// METADATA AND ONLY METADATA: execution and validation read these keys, never
// edges. An author may additionally draw applies-when / avoid-when edges to the
// same fixtures for human traversal; those edges are display-only and no
// executor consults them.
//
// THE EDGE DIRECTION IS FIXED, so two authors cannot produce a graph where half
// the checks point the opposite way with nothing to catch it:
//
//	check --avoid-when--> the check_fixture_bad node   (the shape the check fires on is the one to avoid)
//	check --applies-when--> the check_fixture_good node (the conforming near-miss)
//
// The two presence checks are written out in a fixed order rather than driven
// from a map, because map iteration order is randomized and the message a caller
// sees would otherwise vary between identical runs.
func parseFixtures(md map[string]string) (string, string, error) {
	bad := strings.TrimSpace(md[MetaFixtureBad])
	good := strings.TrimSpace(md[MetaFixtureGood])
	if bad == "" {
		return "", "", fmt.Errorf("corpus: %s is required — there is no fixture-exempt check type", MetaFixtureBad)
	}
	if good == "" {
		return "", "", fmt.Errorf("corpus: %s is required — there is no fixture-exempt check type", MetaFixtureGood)
	}
	if bad == good {
		return "", "", fmt.Errorf("corpus: %s and %s name the same node %q — a check that fires and is silent on one node proves nothing", MetaFixtureBad, MetaFixtureGood, bad)
	}
	return bad, good, nil
}

// validateAstBody proves the body is executable against the check's language:
// the pattern must parse and compile, and the optional where-tree must parse and
// name kinds the grammar has. The compiled pattern is discarded immediately —
// admission only needs to know it compiles.
func validateAstBody(c Check) error {
	if strings.TrimSpace(c.Pattern) == "" {
		return fmt.Errorf("corpus: %s=%s requires a non-empty %s", MetaCheckType, CheckAstPattern, MetaDSLPattern)
	}
	pat, err := ast.Parse(c.Pattern)
	if err != nil {
		return fmt.Errorf("corpus: %s does not parse: %w", MetaDSLPattern, err)
	}
	cp, err := ast.Compile(pat, c.Language, "")
	if err != nil {
		return fmt.Errorf("corpus: %s does not compile for %s=%s: %w", MetaDSLPattern, MetaLanguage, c.Language, err)
	}
	cp.Close()
	if len(c.Where) == 0 {
		return nil
	}
	where, err := ast.ParseWhere(c.Where)
	if err != nil {
		return fmt.Errorf("corpus: %s does not parse: %w", MetaCheckWhere, err)
	}
	if err := ast.ValidateWhereKinds(where, c.Language); err != nil {
		return fmt.Errorf("corpus: %s names a kind the %s=%s grammar does not have: %w", MetaCheckWhere, MetaLanguage, c.Language, err)
	}
	return nil
}

// checkTypeVocabulary renders the admitted check types for an error message.
func checkTypeVocabulary() string {
	parts := make([]string, 0, len(admittedCheckTypes))
	for _, t := range admittedCheckTypes {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, ", ")
}

// severityVocabulary renders the admitted severities for an error message,
// derived from the foundation constants so it cannot enumerate a ladder the
// parser does not actually accept.
func severityVocabulary() string {
	parts := make([]string, 0, len(admittedSeverities))
	for _, s := range admittedSeverities {
		parts = append(parts, string(s))
	}
	return strings.Join(parts, ", ")
}
