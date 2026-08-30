// SPDX-License-Identifier: Apache-2.0

package corpus

import (
	"reflect"
	"strings"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// nodeID is the source node id every fixture in this file carries, so the
// identity assertions have something non-empty to compare against.
const nodeID = "practice-node-under-test"

// validMeta is a metadata map that ParseCheck accepts as row 1. Every refusal
// test starts from this and breaks exactly one rule, so a green refusal is
// attributable to the rule under test rather than to some other missing key.
func validMeta() map[string]string {
	return map[string]string{
		MetaCheckType:   string(CheckAstPattern),
		MetaSeverity:    string(foundation.SeverityWarning),
		MetaLanguage:    string(treesitter.LangGo),
		MetaDSLPattern:  "defer $X.Close()",
		MetaFixtureBad:  "fixture-fires-here",
		MetaFixtureGood: "fixture-silent-here",
	}
}

func node(md map[string]string) *knowledgev1.Node {
	return &knowledgev1.Node{Id: nodeID, Type: "finding", Metadata: md}
}

// mustRefuse asserts the error row (row 4): an error, isCheck false, and a zero
// Check — a refusal that leaked a half-populated Check would let a caller act on
// input the parser rejected.
func mustRefuse(t *testing.T, md map[string]string, wantSubstr string) error {
	t.Helper()
	got, isCheck, err := ParseCheck(node(md))
	if err == nil {
		t.Fatalf("ParseCheck admitted a node it must refuse: isCheck=%v check=%+v", isCheck, got)
	}
	if isCheck {
		t.Errorf("refused node reported isCheck=true")
	}
	if !reflect.DeepEqual(got, Check{}) {
		t.Errorf("refused node returned a non-zero Check: %+v", got)
	}
	if wantSubstr != "" && !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error %q does not name %q", err.Error(), wantSubstr)
	}
	return err
}

// --- Group A: the discriminant and the required fields ---------------------

func TestParseCheck_RejectsUnknownCheckType(t *testing.T) {
	md := validMeta()
	md[MetaCheckType] = "regex_pattern"
	err := mustRefuse(t, md, MetaCheckType)
	// The refusal enumerates the closed set, so an author learns the vocabulary
	// from the error rather than from the source.
	for _, want := range admittedCheckTypes {
		if !strings.Contains(err.Error(), string(want)) {
			t.Errorf("refusal %q does not enumerate %q", err.Error(), want)
		}
	}
}

func TestParseCheck_LLMOnlyExcludesCheckKeys(t *testing.T) {
	for _, key := range llmOnlyExcludedKeys {
		t.Run(key, func(t *testing.T) {
			md := map[string]string{MetaLLMOnly: llmOnlyTrue, key: "anything"}
			mustRefuse(t, md, key)
		})
	}
	t.Run("non-true value", func(t *testing.T) {
		mustRefuse(t, map[string]string{MetaLLMOnly: "yes"}, MetaLLMOnly)
	})
}

func TestParseCheck_RequiresFixturesAndBody(t *testing.T) {
	t.Run("missing bad fixture", func(t *testing.T) {
		md := validMeta()
		delete(md, MetaFixtureBad)
		mustRefuse(t, md, MetaFixtureBad)
	})
	t.Run("missing good fixture", func(t *testing.T) {
		md := validMeta()
		delete(md, MetaFixtureGood)
		mustRefuse(t, md, MetaFixtureGood)
	})
	t.Run("missing body", func(t *testing.T) {
		md := validMeta()
		delete(md, MetaDSLPattern)
		mustRefuse(t, md, MetaDSLPattern)
	})
	t.Run("blank body", func(t *testing.T) {
		md := validMeta()
		md[MetaDSLPattern] = "   "
		mustRefuse(t, md, MetaDSLPattern)
	})
}

// TestParseCheck_NoCheckTypeIsNotACheck pins row 3 apart from rows 2 and 4: a
// node carrying a check-shaped key but no check_type is ordinary content, so it
// is neither a check nor an error, and the returned Check is fully zero — an ID
// or an LLMOnly leaking through here is what would make row 3 indistinguishable
// from row 2 at the read surface.
func TestParseCheck_NoCheckTypeIsNotACheck(t *testing.T) {
	md := map[string]string{
		MetaDSLPattern: "defer $X.Close()",
		MetaSeverity:   string(foundation.SeverityCritical),
	}
	got, isCheck, err := ParseCheck(node(md))
	if err != nil {
		t.Fatalf("a node with no check_type must not error: %v", err)
	}
	if isCheck {
		t.Errorf("a node with no check_type reported isCheck=true")
	}
	if !reflect.DeepEqual(got, Check{}) {
		t.Errorf("row 3 must return the zero Check, got %+v", got)
	}
	if got.LLMOnly {
		t.Errorf("row 3 must not be marked LLMOnly")
	}
	if got.ID != "" {
		t.Errorf("row 3 must not carry an ID, got %q", got.ID)
	}
}

// --- Group B: the value vocabularies and the body --------------------------

func TestParseCheck_RefusesUnknownSeverity(t *testing.T) {
	md := validMeta()
	md[MetaSeverity] = "fatal"
	err := mustRefuse(t, md, MetaSeverity)
	// The enumeration is rendered from the foundation constants, so this also
	// pins that the message cannot advertise a ladder the parser rejects.
	for _, want := range admittedSeverities {
		if !strings.Contains(err.Error(), string(want)) {
			t.Errorf("refusal %q does not enumerate %q", err.Error(), want)
		}
	}
}

// TestParseCheck_RefusesDeniedLanguage uses PHP deliberately: it IS in the
// tree-sitter grammar registry, so it clears the grammar check and can only be
// refused by the ast deny-set rule this test exists to pin. A language with no
// grammar at all would go green here for the wrong reason.
func TestParseCheck_RefusesDeniedLanguage(t *testing.T) {
	if _, ok := treesitter.LanguageGrammar(treesitter.LangPHP); !ok {
		t.Fatalf("php has no registered grammar, so this test cannot discriminate the deny-set rule from the grammar rule")
	}
	md := validMeta()
	md[MetaLanguage] = string(treesitter.LangPHP)
	mustRefuse(t, md, MetaLanguage)

	t.Run("unregistered grammar is also refused", func(t *testing.T) {
		md := validMeta()
		md[MetaLanguage] = "cobol"
		mustRefuse(t, md, MetaLanguage)
	})
}

func TestParseCheck_RefusesUncompilablePattern(t *testing.T) {
	md := validMeta()
	md[MetaDSLPattern] = "func $N( {"
	mustRefuse(t, md, MetaDSLPattern)

	t.Run("where-tree naming a kind the grammar lacks", func(t *testing.T) {
		md := validMeta()
		md[MetaCheckWhere] = `{"kind":{"of":"X","is":"not_a_real_node_kind"}}`
		mustRefuse(t, md, MetaCheckWhere)
	})
}

func TestParseCheck_RefusesIdenticalFixtures(t *testing.T) {
	md := validMeta()
	md[MetaFixtureGood] = md[MetaFixtureBad]
	mustRefuse(t, md, MetaFixtureBad)
}

// --- Group C: the return table's identity and lane rows --------------------

// TestParseCheck_CarriesNodeID pins row 1, and is the known-positive control for
// every refusal above: the parser admits a well-formed check, so a green
// refusal set cannot be explained by a parser that refuses everything.
func TestParseCheck_CarriesNodeID(t *testing.T) {
	got, isCheck, err := ParseCheck(node(validMeta()))
	if err != nil {
		t.Fatalf("a well-formed check must be admitted: %v", err)
	}
	if !isCheck {
		t.Fatalf("a well-formed check must report isCheck=true")
	}
	if got.ID != nodeID {
		t.Errorf("Check.ID = %q, want the source node id %q", got.ID, nodeID)
	}
	if got.LLMOnly {
		t.Errorf("an executable check must not be marked LLMOnly")
	}
	if got.Type != CheckAstPattern {
		t.Errorf("Check.Type = %q, want %q", got.Type, CheckAstPattern)
	}
	if got.Severity != foundation.SeverityWarning {
		t.Errorf("Check.Severity = %q, want %q", got.Severity, foundation.SeverityWarning)
	}
	if got.Language != treesitter.LangGo {
		t.Errorf("Check.Language = %q, want %q", got.Language, treesitter.LangGo)
	}
	if got.FixtureBad == "" || got.FixtureGood == "" || got.FixtureBad == got.FixtureGood {
		t.Errorf("fixtures did not survive parsing: bad=%q good=%q", got.FixtureBad, got.FixtureGood)
	}
}

// TestParseCheck_LLMOnlyReturnsMarkedNotCheck pins row 2 apart from row 3: the
// node is not executable, but it IS a member of the needs-LLM-judgment lane, and
// a consumer can only report that lane honestly if the marker and the id survive
// the parse.
func TestParseCheck_LLMOnlyReturnsMarkedNotCheck(t *testing.T) {
	got, isCheck, err := ParseCheck(node(map[string]string{
		MetaLLMOnly: llmOnlyTrue, MetaLanguage: string(treesitter.LangGo),
	}))
	if err != nil {
		t.Fatalf("a valid llm_only node must not error: %v", err)
	}
	if isCheck {
		t.Errorf("an llm_only node is not an executable check")
	}
	if !got.LLMOnly {
		t.Errorf("an accepted llm_only node must be distinguishable from prose, got %+v", got)
	}
	if got.ID != nodeID {
		t.Errorf("Check.ID = %q, want the source node id %q", got.ID, nodeID)
	}
	if got.Language != treesitter.LangGo {
		t.Errorf("row 2 must carry the language its guidance is about, got %q", got.Language)
	}
	// Row 2 still populates no CHECK-BODY field: the marker and the descriptive
	// language, never a type, pattern or fixture.
	if got.Type != "" || got.Pattern != "" || got.FixtureBad != "" {
		t.Errorf("row 2 must populate no check-body field, got %+v", got)
	}
}

// TestParseCheck_LLMOnlyRequiresLanguage is the negative leg of the rule above,
// and it guards a SILENT failure rather than a loud one.
//
// Checks and llm_only prose share ONE graph, and every corpus read narrows by a
// language metadata predicate. An llm_only node with no language therefore
// matches no predicate and is handed to nobody — the needs-LLM-judgment lane
// would empty out with no error anywhere, which is precisely the collapse the
// lane exists to prevent. Refusing at admission converts that silence into a
// message the author can act on.
func TestParseCheck_LLMOnlyRequiresLanguage(t *testing.T) {
	// CONTROL: the same node WITH a language is admitted, so the refusal below
	// cannot be a parser that rejects every llm_only node.
	if _, _, err := ParseCheck(node(map[string]string{
		MetaLLMOnly: llmOnlyTrue, MetaLanguage: string(treesitter.LangGo),
	})); err != nil {
		t.Fatalf("control: a languaged llm_only node must be admitted, got %v", err)
	}

	_, _, err := ParseCheck(node(map[string]string{MetaLLMOnly: llmOnlyTrue}))
	if err == nil {
		t.Fatal("an llm_only node with no language must be REFUSED — it would be invisible to " +
			"every language-scoped scan, silently emptying the needs-judgment lane")
	}
	if !strings.Contains(err.Error(), MetaLanguage) {
		t.Errorf("the refusal must name the missing key, got %q", err)
	}
}
