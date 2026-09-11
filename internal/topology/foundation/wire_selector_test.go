// SPDX-License-Identifier: Apache-2.0

package foundation

// wire_selector_test.go — the two selector composers in this package agree with
// each other and with the server's per-family field partition.
//
// WHY IT EXISTS. scopePayload (the Compile-based browse) and graphTarget (the six
// raw-plan helpers) each used to carry a practice arm that put the caller's name
// on `language`, because the practice family held one graph per language and a
// derived selector carries no instance field. Those graphs are retired, the
// server refuses the field, and BOTH arms were removed — and restoring either one
// reddened nothing in this module. The two sibling override sites this change
// also edited were each observed: projects/render's graphTarget by
// TestGraphTarget_PerFamilySelectorField and manage_status_coverage's
// statusGraphTarget by TestCoverageSelectors_AcceptedByServerPolicy. This package
// had no such test, so a removal the prefill's matrix listed as its own row was
// EXPECTED behavior with nothing reading it.
//
// IT IS SHAPED ON projects/render's TestGraphTarget_PerFamilySelectorField, which
// is the pattern already in the tree for this exact question.
//
// THE CODE FAMILY IS THE SAME-RUN CONTROL, in the same test rather than beside
// it. Without it a composer that returned an empty payload and a bare selector
// for EVERY family satisfies every practice assertion here, and that composer
// would break every code-graph topology probe.

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestPracticeSelectorComposers_CarryNoInstanceField pins both composers on both
// practice spellings, with code and a name-keyed family as the controls.
func TestPracticeSelectorComposers_CarryNoInstanceField(t *testing.T) {
	// BOTH SPELLINGS: a practice ref naming the combined graph and one naming a
	// graph that no longer exists. Neither may put anything on the selector, and
	// the second is the one the removed arms fired on — an arm conditioned on
	// `name != ""` is invisible to the first spelling alone.
	for _, name := range []string{"", "default", "go"} {
		t.Run("scopePayload/name="+name, func(t *testing.T) {
			payload := scopePayload(kgtypes.GraphPractice, name)
			if got := payload["graph"]; got != string(kgtypes.GraphPractice) {
				t.Errorf("scopePayload names the family: got graph=%v, want %q", got, kgtypes.GraphPractice)
			}
			for _, key := range []string{"language", "name", "repo", "account", "branch"} {
				if v, ok := payload[key]; ok {
					t.Errorf("scopePayload(practice, %q) carries %s=%v; practice addresses no instance and the server refuses every one of these fields",
						name, key, v)
				}
			}
		})

		t.Run("graphTarget/name="+name, func(t *testing.T) {
			sel := graphTarget(kgtypes.GraphPractice, name)
			if sel == nil {
				t.Fatalf("graphTarget(practice, %q) returned nil; a practice read still addresses the family", name)
			}
			if sel.GetGraph() != string(kgtypes.GraphPractice) {
				t.Errorf("graphTarget names the family: got %q, want %q", sel.GetGraph(), kgtypes.GraphPractice)
			}
			if got := sel.GetLanguage(); got != "" {
				t.Errorf("graphTarget(practice, %q) carries language=%q; the server refuses that field on every practice request",
					name, got)
			}
			if got := sel.GetName(); got != "" {
				t.Errorf("graphTarget(practice, %q) carries name=%q; the practice policy refuses a name outside its root aliases",
					name, got)
			}
			if got := sel.GetRepo(); got != "" {
				t.Errorf("graphTarget(practice, %q) carries repo=%q", name, got)
			}
		})
	}

	// THE PAIR MUST AGREE, which is what wire.go's own comment demands of them:
	// one is the payload-key twin of the other, so a fix applied to one and not
	// the other is a selector that scopes differently depending on which helper
	// the caller reached.
	t.Run("the_two_composers_agree_on_practice", func(t *testing.T) {
		for _, name := range []string{"", "default", "go"} {
			_, payloadHasLanguage := scopePayload(kgtypes.GraphPractice, name)["language"]
			targetHasLanguage := graphTarget(kgtypes.GraphPractice, name).GetLanguage() != ""
			if payloadHasLanguage != targetHasLanguage {
				t.Errorf("name=%q: scopePayload carries a language=%v while graphTarget carries one=%v; the two must agree",
					name, payloadHasLanguage, targetHasLanguage)
			}
		}
	})

	// THE CONTROLS, in the same run. A composer that dropped EVERY instance field
	// satisfies every assertion above and breaks these.
	t.Run("control_code_still_carries_its_repo", func(t *testing.T) {
		payload := scopePayload(kgtypes.GraphCode, "myrepo")
		if got := payload["repo"]; got != "myrepo" {
			t.Errorf("control: scopePayload(code, myrepo) must carry repo=myrepo, got %v", got)
		}
		sel := graphTarget(kgtypes.GraphCode, "myrepo")
		if got := sel.GetRepo(); got != "myrepo" {
			t.Errorf("control: graphTarget(code, myrepo) must carry repo=myrepo, got %q", got)
		}
		if got := sel.GetLanguage(); got != "" {
			t.Errorf("control: a code selector carries no language, got %q", got)
		}
	})

	t.Run("control_a_name_keyed_family_still_carries_its_name", func(t *testing.T) {
		payload := scopePayload(kgtypes.GraphWebRaw, "docs-site")
		if got := payload["name"]; got != "docs-site" {
			t.Errorf("control: scopePayload(web, docs-site) must carry name=docs-site, got %v", got)
		}
		sel := graphTarget(kgtypes.GraphWebRaw, "docs-site")
		if got := sel.GetName(); got != "docs-site" {
			t.Errorf("control: graphTarget(web, docs-site) must carry name=docs-site, got %q", got)
		}
	})
}
