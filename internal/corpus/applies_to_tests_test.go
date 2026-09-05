// SPDX-License-Identifier: Apache-2.0

// applies_to_tests_test.go — the declaration that a check's defect class lives
// in test files.
//
// THE VALUE VOCABULARY IS CLOSED and the key is validated on EVERY node, not
// only on the executable arm. ParseCheck returns on the llm_only branch before
// the check body is ever parsed, so a declaration validated only in the body
// parser is unvalidated on every llm_only node — and the live Go corpus has
// eight of them. Each arm here carries the same-run control that tells a working
// rule from a parser that refuses everything it is shown.

package corpus

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

func TestParseCheck_AppliesToTestsAdmitsOnlyTrue(t *testing.T) {
	t.Run("present as true sets the declaration", func(t *testing.T) {
		md := validMeta()
		md[MetaAppliesToTests] = "true"
		got, isCheck, err := ParseCheck(node(md))
		if err != nil {
			t.Fatalf("a declaration of true must be admitted: %v", err)
		}
		if !isCheck {
			t.Fatalf("the node is still an executable check")
		}
		if !got.AppliesToTests {
			t.Errorf("Check.AppliesToTests = false; the declaration must reach the executor that widens the walk")
		}
	})

	t.Run("absent is legal and means false", func(t *testing.T) {
		got, isCheck, err := ParseCheck(node(validMeta()))
		if err != nil || !isCheck {
			t.Fatalf("the control node must still be admitted: isCheck=%v err=%v", isCheck, err)
		}
		if got.AppliesToTests {
			t.Errorf("an absent key must not read as a declaration")
		}
	})

	// "false" is refused rather than read as a false, which is the llm_only
	// marker's own rule: one admitted literal, and absence is the other state.
	// A second spelling of false is a second vocabulary to keep in agreement.
	for _, bad := range []string{"false", "yes", "TRUE", "1", ""} {
		t.Run("refuses "+bad, func(t *testing.T) {
			md := validMeta()
			md[MetaAppliesToTests] = bad
			err := mustRefuse(t, md, MetaAppliesToTests)
			if !strings.Contains(err.Error(), `"`+bad+`"`) {
				t.Errorf("the refusal must echo the offending value, got %q", err.Error())
			}
			if !strings.Contains(err.Error(), `"true"`) {
				t.Errorf("and name the admitted vocabulary, got %q", err.Error())
			}
		})
	}
}

// TestParseCheck_AppliesToTestsRefusedOnLLMOnly is the arm the executable-arm
// test cannot reach. An llm_only check never walks a tree, so a walk-scope
// control on one is a control its executor ignores — the failure the topology
// dispatcher's honoring allowlist exists to prevent — and the declaration
// therefore joins the exclusivity sweep rather than being quietly accepted.
func TestParseCheck_AppliesToTestsRefusedOnLLMOnly(t *testing.T) {
	llmOnly := func(extra map[string]string) map[string]string {
		md := map[string]string{
			MetaLLMOnly:  llmOnlyTrue,
			MetaLanguage: string(treesitter.LangGo),
		}
		maps.Copy(md, extra)
		return md
	}

	// THE FALSIFYING CONTROL, run first: the same node WITHOUT the key is
	// admitted as an llm_only entry. Without it, a parser that refused every
	// llm_only node would pass the refusals below.
	if _, isCheck, err := ParseCheck(node(llmOnly(nil))); err != nil || isCheck {
		t.Fatalf("control: an llm_only node without the declaration must be admitted: isCheck=%v err=%v", isCheck, err)
	}

	t.Run("refused with the admitted value", func(t *testing.T) {
		err := mustRefuse(t, llmOnly(map[string]string{MetaAppliesToTests: "true"}), MetaAppliesToTests)
		if !strings.Contains(err.Error(), MetaLLMOnly) {
			t.Errorf("the refusal must name both keys so the author knows which pair collided, got %q", err.Error())
		}
	})

	t.Run("refused with a malformed value", func(t *testing.T) {
		mustRefuse(t, llmOnly(map[string]string{MetaAppliesToTests: "sometimes"}), MetaAppliesToTests)
	})
}

// TestParseCheck_AppliesToTestsValidatedOnANodeThatIsNeitherLaneDrives the arm
// the parse ORDER actually owns, and nothing else does.
//
// WHAT THE ORDER DECIDES, precisely. The llm_only arm is refused by the
// exclusivity sweep on PRESENCE, so moving the value check behind that branch
// leaves those tests green. What running AHEAD of the branch adds is value
// validation on the third population: a node that reaches neither parseCheckBody
// nor parseLLMOnly, because it carries no check_type and no llm_only. ParseCheck
// runs for every node the checks graph returns, so that population is real, and
// behind the branch such a node is classified not-a-check with a malformed
// declaration on it rather than refused.
//
// THE CONTROL IS WHAT MAKES THIS A TEST OF THE ORDER rather than of the parser
// at large: the same node with the key ABSENT must still be classified
// not-a-check with no error, so a parser that refused every non-check node would
// fail here.
func TestParseCheck_AppliesToTestsValidatedOnANodeThatIsNeitherLane(t *testing.T) {
	t.Run("no check_type and no llm_only: a malformed declaration is still refused", func(t *testing.T) {
		md := map[string]string{
			MetaLanguage:       string(treesitter.LangGo),
			MetaAppliesToTests: "sometimes",
		}
		got, isCheck, err := ParseCheck(node(md))
		if err == nil {
			t.Fatalf("a malformed declaration must be refused on every node the corpus load parses, got isCheck=%v check=%+v", isCheck, got)
		}
		if isCheck {
			t.Errorf("a node with no check_type is not an executable check")
		}
		if !strings.Contains(err.Error(), MetaAppliesToTests) {
			t.Errorf("the refusal must name the key, got %q", err)
		}
		if !strings.Contains(err.Error(), `"sometimes"`) {
			t.Errorf("and echo the offending value, got %q", err)
		}
	})

	t.Run("control: the same node without the key is classified, not refused", func(t *testing.T) {
		md := map[string]string{MetaLanguage: string(treesitter.LangGo)}
		got, isCheck, err := ParseCheck(node(md))
		if err != nil {
			t.Fatalf("a node that is simply not a check must be classified, not refused: %v", err)
		}
		if isCheck {
			t.Errorf("a node with no check_type is not an executable check")
		}
		if !reflect.DeepEqual(got, Check{}) {
			t.Errorf("a not-a-check return carries no populated Check, got %+v", got)
		}
	})
}

// TestParseCheck_AppliesToTestsRefusedForLanguageWithNoTestConvention follows
// the ast tool's own hard refusal exactly. A language ast carries no test-file
// convention for filters nothing either way, so the declaration on such a check
// is a documented control that does nothing — the one shape this whole surface
// exists to remove.
func TestParseCheck_AppliesToTestsRefusedForLanguageWithNoTestConvention(t *testing.T) {
	// CONTROL: the same language WITHOUT the declaration is admitted, so the
	// refusal is attributable to the declaration and not to the language.
	base := validMeta()
	base[MetaLanguage] = string(treesitter.LangRust)
	base[MetaSeverity] = string(foundation.SeverityWarning)
	base[MetaDSLPattern] = "let $X = $Y;"
	if _, isCheck, err := ParseCheck(node(base)); err != nil || !isCheck {
		t.Fatalf("control: a rust check without the declaration must be admitted: isCheck=%v err=%v", isCheck, err)
	}

	md := validMeta()
	md[MetaLanguage] = string(treesitter.LangRust)
	md[MetaDSLPattern] = "let $X = $Y;"
	md[MetaAppliesToTests] = "true"
	err := mustRefuse(t, md, MetaAppliesToTests)
	if !strings.Contains(err.Error(), string(treesitter.LangRust)) {
		t.Errorf("the refusal must name the offending language, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "go") {
		t.Errorf("and list the languages that do carry a convention, got %q", err.Error())
	}
}
