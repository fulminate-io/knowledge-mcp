// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// THIS TEST IS THE COMPILER FOR A SEAM NO COMPILER SPANS.
//
// The coverage collector builds a graph selector CLIENT-side and the server
// decides whether to accept it. They are different modules: nothing type-checks
// the two together, and an in-process test on either side proves only that side
// is self-consistent. That is exactly how every write to the checks graph
// reached production broken — the client put an instance name on a selector for
// a family whose server-side policy consumed none, and both halves had passing
// tests.
//
// So this test reads THE SERVER'S OWN POLICY TABLE AS DATA and drives the real
// client-side construction against it. It is not a hand-written table of
// expectations: a hand-written table checked against another hand-written table
// detects disagreement between the two and can never detect a MISSING entry,
// because a family absent from both is absent from the comparison too.
//
// It drives from kgtypes.SyncEligibleGraphTypes(), the same enumeration the
// collector itself walks, so a newly sync-eligible family is covered the moment
// it is added rather than when someone remembers to extend a list.

// serverSelectorPolicySource is the server file this test consumes as data,
// relative to the repo root.
const serverSelectorPolicySource = "cmd/knowledge-server/internal/tools/tools_graph_routing_selector.go"

// fieldPolicy is the subset of the server's selectorFieldPolicy this test needs:
// which selector fields the family consumes.
type fieldPolicy struct {
	consumes map[string]bool
}

// TestCoverageSelectors_AcceptedByServerPolicy asserts that for every
// sync-eligible graph type, the selector the coverage collector actually builds
// sets no field the server's policy would refuse.
//
// UNVERIFIED IN CI: this test needs the SERVER's source, and cmd/knowledge-server
// is not copied to the public mirror by the sync script. In the mirror's CI the
// file is absent and this cross-module agreement goes unchecked — there is no
// way to check it there, because the other side of the seam is not present. It
// runs in this repo, which is where the two modules live together and where a
// divergence is introduced.
func TestCoverageSelectors_AcceptedByServerPolicy(t *testing.T) {
	root := repoRootForTest(t)
	policySrc := filepath.Join(root, serverSelectorPolicySource)
	if _, err := os.Stat(policySrc); err != nil {
		t.Skipf("server selector policy source not present at %s (expected in the public mirror, which does not carry cmd/knowledge-server): %v",
			serverSelectorPolicySource, err)
	}

	policies, aliases := parseServerSelectorPolicy(t, policySrc)
	if len(policies) == 0 {
		t.Fatal("parsed zero policy rows from the server source, so this test measured nothing")
	}

	types := kgtypes.SyncEligibleGraphTypes()
	if len(types) == 0 {
		t.Fatal("SyncEligibleGraphTypes returned nothing, so this test measured nothing")
	}

	checked := 0
	for _, gt := range types {
		policy, ok := policies[string(gt)]
		if !ok {
			// Absent from the table means registered-custom, which consumes a
			// name by design. Not a violation, but worth surfacing: a builtin
			// family missing its row is how a selector reaches the default arm.
			t.Logf("note: %q has no row in the server policy table (treated as registered-custom)", gt)
			continue
		}
		for _, name := range reachableNames(policy) {
			target := newCoverageTarget(gt, name, false)
			for field, value := range setSelectorFields(target) {
				checked++
				if policy.consumes[field] {
					continue
				}
				if field == "name" && aliasAccepts(aliases, string(gt), value) {
					continue
				}
				t.Errorf("coverage target for graph=%q name=%q builds a selector setting %s=%q, which the server's policy for that family does not consume — this is the shape that made every checks write fail in production",
					gt, name, field, value)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no selector field was examined across any sync-eligible type, so this test measured nothing")
	}

	// KNOWN-POSITIVE CONTROL, run against the REAL parsed server data. Every
	// assertion above is a pass condition, and a predicate that accepted
	// everything would satisfy all of them. This drives the same two-part
	// predicate the loop uses — is the field consumed, and if it is a name, does
	// an alias admit it — with a combination the server genuinely refuses, and
	// requires it to say NO.
	//
	// It is deliberately NOT done by editing graphsel or the server policy: both
	// belong to other lanes, and a control that mutates a shared file to prove a
	// point is a control that can be left behind.
	checksPolicy, ok := policies["checks"]
	if !ok {
		t.Fatal("the server policy table has no checks row, so the control cannot run")
	}
	if checksPolicy.consumes["name"] {
		t.Fatal("checks is supposed to consume no instance field; the control's premise is gone")
	}
	if aliasAccepts(aliases, "checks", "instance-x") {
		t.Fatal("an arbitrary instance name must not be admitted for checks by any alias list")
	}
	// And the positive half of the same predicate: the value that broke
	// production IS admitted now, which is the fix this guard protects.
	if !aliasAccepts(aliases, "checks", "default") {
		t.Fatal(`the server must admit name="default" for checks — that exemption is what unblocked writes to the checks graph`)
	}

	t.Logf("cross-module selector check: %d sync-eligible types, %d set fields examined against the server's own policy table", len(types), checked)
}

// reachableNames is the set of instance names the enumeration can actually hand
// newCoverageTarget for a family, DERIVED FROM THE SERVER'S POLICY rather than
// from a hand-written classification.
//
// A family whose policy consumes no instance-selecting field is a singleton: the
// server resolves a hard-coded "default" graph, so the enumeration yields the
// empty string or "default" and nothing else. "default" is the value that broke
// production for checks, so it is the one that matters most here.
//
// AN ASSERTION IS ONLY AS STRONG AS THE INPUT SPACE ITS FIXTURES SPAN — and
// spanning MORE than the producer can emit is the opposite error, which this
// function exists to avoid. Driving an arbitrary instance name at a singleton
// reported knowledge and linkage as violations, and neither is reachable: the
// collector skips knowledge from enumeration entirely and emits it with an empty
// name, and a singleton's enumeration returns its hard-coded label.
//
// WHAT THIS THEREFORE DOES NOT COVER, stated rather than implied: if a
// singleton's enumeration ever returned an arbitrary name, knowledge and linkage
// would build a selector the server refuses, because only checks drops the name
// client-side via its FieldNone arm. That hazard is real and out of this test's
// span; closing it would mean giving every singleton the FieldNone treatment.
func reachableNames(policy fieldPolicy) []string {
	if len(policy.consumes) == 0 {
		return []string{"", "default"}
	}
	return []string{"", "default", "instance-x"}
}

// setSelectorFields returns the selector fields the coverage target actually
// set, keyed by the server's own field vocabulary.
func setSelectorFields(target coverageTarget) map[string]string {
	sel := target.target
	out := map[string]string{}
	if v := sel.GetRepo(); v != "" {
		out["repo"] = v
	}
	if v := sel.GetAccount(); v != "" {
		out["account"] = v
	}
	if v := sel.GetLanguage(); v != "" {
		out["language"] = v
	}
	if v := sel.GetName(); v != "" {
		out["name"] = v
	}
	if v := sel.GetBranch(); v != "" {
		out["branch"] = v
	}
	return out
}

// aliasAccepts reports whether the server exempts this family's name value via
// one of its root-name alias lists.
func aliasAccepts(aliases map[string][]string, graph, value string) bool {
	return slices.Contains(aliases[graph], value)
}

// parseServerSelectorPolicy reads the server's policy table and its root-name
// alias lists out of its SOURCE, so this test tracks the server's real rule
// rather than a copy of it that can drift.
func parseServerSelectorPolicy(t *testing.T, path string) (map[string]fieldPolicy, map[string][]string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse server selector policy: %v", err)
	}

	// consumesX field name -> the server's own field vocabulary.
	fieldOf := map[string]string{
		"consumesRepo":     "repo",
		"consumesAccount":  "account",
		"consumesLanguage": "language",
		"consumesName":     "name",
		"consumesBranch":   "branch",
	}
	policies := map[string]fieldPolicy{}
	aliases := map[string][]string{}
	// The alias variable names, mapped to the families they govern. The
	// knowledge list governs both spellings of that family.
	aliasVars := map[string][]string{
		"knowledgeRootNameAliases": {"", "knowledge"},
		"linkageRootNameAliases":   {"linkage"},
		"checksRootNameAliases":    {"checks"},
	}

	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		switch name := spec.Names[0].Name; {
		case name == "selectorFieldPolicies":
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := stringLit(kv.Key)
				if !ok {
					continue
				}
				policies[key] = fieldPolicy{consumes: consumedFields(kv.Value, fieldOf)}
			}
		case aliasVars[name] != nil:
			var vals []string
			for _, el := range lit.Elts {
				if s, ok := stringLit(el); ok {
					vals = append(vals, s)
				}
			}
			for _, fam := range aliasVars[name] {
				aliases[fam] = vals
			}
		}
		return true
	})
	return policies, aliases
}

// consumedFields reads the `consumesX: true` members of one policy literal.
func consumedFields(v ast.Expr, fieldOf map[string]string) map[string]bool {
	out := map[string]bool{}
	lit, ok := v.(*ast.CompositeLit)
	if !ok {
		return out
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		val, ok := kv.Value.(*ast.Ident)
		if !ok || val.Name != "true" {
			continue
		}
		if field, ok := fieldOf[key.Name]; ok {
			out[field] = true
		}
	}
	return out
}
