// SPDX-License-Identifier: Apache-2.0

// Package tools — the OVERLAY dimension of manage(repair_edges): a repo-scoped
// repair must reach the base graph AND every branch overlay, whichever of the two
// name forms the catalog reported, and a named branch must NARROW to exactly one
// overlay whichever spelling the operator typed.

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

const (
	repairOverlayRepo   = "agent"
	repairOverlayBranch = "launch-fixes"
)

// repairTwoLayerFake seeds the two-layer fixture both overlay tests share: repo
// "agent" with a BASE layer and one overlay layer under the BARE key
// "launch-fixes" — the key the server would actually resolve, regardless of which
// form the CATALOG reported it as (catalogKey).
//
// The two layers' fossils are DELIBERATELY DISTINCT concrete values (different
// source files, different targets, different homes), so a fixture that collapsed
// both layers onto one map could not pass. Each layer also carries a same-file
// CONTAINS control that must survive the repair.
func repairTwoLayerFake(catalogKey string) *repairEdgesFake {
	f := newRepairEdgesFake()
	f.overlayKeys[repairOverlayRepo] = []string{catalogKey}

	base := f.layer(repairEdgesLayerKey{Repo: repairOverlayRepo})
	base.files = []*knowledgev1.Node{repairFileNode("src/base.ts")}
	base.symbols = map[string]*knowledgev1.Node{
		"pkg/x.go:BaseSym":  repairSymbolNode("pkg/x.go:BaseSym", "pkg/x.go"),
		"src/base.ts:Local": repairSymbolNode("src/base.ts:Local", "src/base.ts"),
	}
	base.edges = []*knowledgev1.Edge{
		repairContainsEdge("src/base.ts", "pkg/x.go:BaseSym"),  // the base layer's fossil
		repairContainsEdge("src/base.ts", "src/base.ts:Local"), // same-file control
	}

	ovl := f.layer(repairEdgesLayerKey{Repo: repairOverlayRepo, Branch: repairOverlayBranch})
	ovl.files = []*knowledgev1.Node{repairFileNode("src/ovl.ts")}
	ovl.symbols = map[string]*knowledgev1.Node{
		"pkg/y.go:OverlaySym": repairSymbolNode("pkg/y.go:OverlaySym", "pkg/y.go"),
		"src/ovl.ts:Local":    repairSymbolNode("src/ovl.ts:Local", "src/ovl.ts"),
	}
	ovl.edges = []*knowledgev1.Edge{
		repairContainsEdge("src/ovl.ts", "pkg/y.go:OverlaySym"), // the overlay layer's fossil
		repairContainsEdge("src/ovl.ts", "src/ovl.ts:Local"),    // same-file control
	}
	return f
}

// repairLayerEdgeStrings renders one layer's surviving edges for an equality
// assertion.
func repairLayerEdgeStrings(f *repairEdgesFake, key repairEdgesLayerKey) []string {
	edges := f.layer(key).edges
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.GetFromId()+"->"+e.GetToId())
	}
	return out
}

// TestManageRepairEdges_OverlayFanOutRepairsBaseAndOverlays asserts a repo-scoped
// repair reaches the base graph AND its branch overlay, TABLED OVER THE TWO
// CATALOG KEY FORMS the two backends genuinely report for the same overlay: the
// cloud composed "agent@launch-fixes" key and the OSS bare "launch-fixes" name.
// Both cases seed the SAME fixture and must produce the SAME outcome — that
// equivalence IS the assertion, because the resolver must be blind to which form
// arrived.
//
// The bare_OSS_key case is the red-first reproduction for the OSS-DROP defect: a
// resolver copying appendOverlayTargets' skip-the-non-splitting-key handling
// produces ZERO overlay targets for a bare key, so the overlay is never read and
// its fossil survives. The cloud_form_key case is the red-first reproduction for
// the DOUBLED-PREFIX defect: skipping normalization sends
// Branch="agent@launch-fixes", the server composes "agent@agent@launch-fixes",
// Scope fails and resolveCode SILENTLY falls back to the base — so the repair
// reports zero remaining while the overlay stays contaminated.
func TestManageRepairEdges_OverlayFanOutRepairsBaseAndOverlays(t *testing.T) {
	cases := []struct {
		name       string
		catalogKey string
	}{
		{"cloud form key", repairOverlayRepo + "@" + repairOverlayBranch},
		{"bare OSS key", repairOverlayBranch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := repairTwoLayerFake(tc.catalogKey)
			baseKey := repairEdgesLayerKey{Repo: repairOverlayRepo}
			ovlKey := repairEdgesLayerKey{Repo: repairOverlayRepo, Branch: repairOverlayBranch}

			handled, res := repairEdgesCall(t, f,
				`{"operation":"repair_edges","graph":"code","name":"agent","execute":true}`)
			require.True(t, handled)
			require.False(t, res.IsError, "repair_edges execute: %s", toolResultText(res))
			body := toolResultText(res)

			// (1) BOTH layers were addressed, and no Branch carried a composed key.
			assert.Contains(t, f.servedKeys, baseKey, "the base layer must be read")
			assert.Contains(t, f.servedKeys, ovlKey,
				"the overlay layer must be read — a resolver that drops this key repairs nothing on that layer")
			for _, k := range f.servedKeys {
				assert.NotContains(t, k.Branch, "@",
					"Branch is the BARE overlay name; a composed key doubles the base prefix and silently resolves to the base")
			}

			// (2) an unlink was issued against EACH layer.
			assert.Contains(t, f.mutatedKeys, baseKey, "the base layer's fossil is unlinked at the base")
			assert.Contains(t, f.mutatedKeys, ovlKey, "the overlay layer's fossil is unlinked at the overlay")

			// (3) both fossils are gone and both same-file controls survive.
			assert.Equal(t, []string{"src/base.ts->src/base.ts:Local"},
				repairLayerEdgeStrings(f, baseKey), "the base fossil is gone; its same-file control survives")
			assert.Equal(t, []string{"src/ovl.ts->src/ovl.ts:Local"},
				repairLayerEdgeStrings(f, ovlKey), "the overlay fossil is gone; its same-file control survives")

			// (4) the verify-after re-enumeration reports zero PER LAYER — the
			// ticket's "post-repair read UNDER the auto-stamped branch reports zero".
			assert.Contains(t, body, "code/agent: 0 fossil(s) remaining after the repair")
			assert.Contains(t, body, "code/agent@launch-fixes: 0 fossil(s) remaining after the repair")

			// (5) the completion claim.
			assert.Contains(t, body, "Repair COMPLETE")
			assert.NotContains(t, body, "PARTIAL SUCCESS")
		})
	}
}

// TestManageRepairEdges_CloudFormOverlayKeyNormalizedToBareBranch is the focused
// unit gate on the NORMALIZATION itself, isolated from the fan-out outcome: with
// the catalog reporting the cloud "agent@launch-fixes" key, the recorded selector
// Branch values must be exactly {"" (base), "launch-fixes" (overlay)} BY EQUALITY.
// A failure here localizes the defect to the normalization rather than to the
// fan-out.
func TestManageRepairEdges_CloudFormOverlayKeyNormalizedToBareBranch(t *testing.T) {
	f := repairTwoLayerFake(repairOverlayRepo + "@" + repairOverlayBranch)

	handled, res := repairEdgesCall(t, f,
		`{"operation":"repair_edges","graph":"code","name":"agent","execute":true}`)
	require.True(t, handled)
	require.False(t, res.IsError, "repair_edges execute: %s", toolResultText(res))

	require.NotEmpty(t, f.servedKeys, "the repair must have read something — a zero here is a vacuous pass")
	seen := map[string]bool{}
	for _, k := range f.servedKeys {
		assert.Equal(t, repairOverlayRepo, k.Repo,
			"the overlay routes via Branch, never by composing onto Repo")
		seen[k.Branch] = true
	}
	branches := make([]string, 0, len(seen))
	for b := range seen {
		branches = append(branches, b)
	}
	assert.ElementsMatch(t, []string{"", repairOverlayBranch}, branches,
		`the cloud "agent@launch-fixes" catalog key must normalize to the bare "launch-fixes"`)
}

// TestManageRepairEdges_BranchNarrowsToOneOverlay asserts a named branch NARROWS
// the repair to exactly that one overlay, TABLED OVER THE TWO OPERATOR ARGUMENT
// FORMS. The operator-supplied branch is the THIRD source of a branch value, and
// it is the one this tool's own report teaches in the composed spelling, because
// the per-layer lines print "code/agent@launch-fixes". Both forms must produce the
// SAME outcome; that equivalence is the assertion.
//
// The composed_branch_arg case is the red-first reproduction: against a resolver
// that forwards a.Branch verbatim the recorded Branch is "agent@launch-fixes", the
// server composes "agent@agent@launch-fixes", Scope fails, resolveCode SILENTLY
// falls back to the base, and the repair prints "Repair COMPLETE" having re-scanned
// the clean base while the overlay stays contaminated.
func TestManageRepairEdges_BranchNarrowsToOneOverlay(t *testing.T) {
	cases := []struct {
		name string
		arg  string
	}{
		{"bare branch arg", repairOverlayBranch},
		{"composed branch arg", repairOverlayRepo + "@" + repairOverlayBranch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := repairTwoLayerFake(repairOverlayRepo + "@" + repairOverlayBranch)
			baseKey := repairEdgesLayerKey{Repo: repairOverlayRepo}
			ovlKey := repairEdgesLayerKey{Repo: repairOverlayRepo, Branch: repairOverlayBranch}

			handled, res := repairEdgesCall(t, f,
				`{"operation":"repair_edges","graph":"code","name":"agent","branch":"`+tc.arg+`","execute":true}`)
			require.True(t, handled)
			require.False(t, res.IsError, "repair_edges execute: %s", toolResultText(res))
			body := toolResultText(res)

			// (1) the ONLY target read is the named overlay — BY EQUALITY, so a
			// forwarded composed value fails loudly. This is also the NARROWING
			// property: treating branch as an ADDITIONAL target would read the base.
			require.NotEmpty(t, f.servedKeys, "the repair must have read the named overlay")
			for _, k := range f.servedKeys {
				assert.Equal(t, ovlKey, k, "a named branch narrows to exactly that one overlay")
			}

			// (2) known-positive control: the base layer really did hold a fossil,
			// and it is STILL there — without it (1) could pass on an empty base.
			assert.Equal(t,
				[]string{"src/base.ts->pkg/x.go:BaseSym", "src/base.ts->src/base.ts:Local"},
				repairLayerEdgeStrings(f, baseKey),
				"the base layer is never read and never mutated by a branch-narrowed repair")

			// (3) an explicitly named overlay needs no enumeration.
			for _, p := range f.queryPlans {
				assert.NotEqual(t, knowledgev1.ReturnMode_RETURN_MODE_GRAPH_NAMES, p.GetReturnMode(),
					"a named overlay is not enumerated — the catalog read would be wasted and wrong")
			}

			// (4) the report names the overlay as a GRAPH IDENTITY.
			assert.Contains(t, body, "code/agent@launch-fixes: 0 fossil(s) remaining after the repair")
			assert.Contains(t, body, "Repair COMPLETE")

			// (5) the PREVIEW's echoed invocation carries the BARE branch, even for
			// the composed argument. Two spellings, two meanings: Label() renders a
			// graph identity, branch: is an argument and is always bare — otherwise
			// this tool's own output teaches the spelling that silently resolves to
			// the base.
			pf := repairTwoLayerFake(repairOverlayRepo + "@" + repairOverlayBranch)
			handled, previewRes := repairEdgesCall(t, pf,
				`{"operation":"repair_edges","graph":"code","name":"agent","branch":"`+tc.arg+`"}`)
			require.True(t, handled)
			require.False(t, previewRes.IsError, "repair_edges preview: %s", toolResultText(previewRes))
			assert.Contains(t, toolResultText(previewRes),
				`manage(operation:"repair_edges", graph:"code", name:"agent", branch:"launch-fixes", execute:true)`,
				"the echoed branch argument is the BARE form, never the operator's raw composed spelling")
			assert.Empty(t, pf.mutations, "a preview issues ZERO mutations")
		})
	}
}

// TestManageRepairEdges_BranchWithoutNameIsALoudError asserts branch set with an
// EMPTY name is refused loudly, issuing zero reads and zero mutations: a branch
// overlay belongs to exactly one repo, so branch without a name selects no graph.
func TestManageRepairEdges_BranchWithoutNameIsALoudError(t *testing.T) {
	f := repairTwoLayerFake(repairOverlayRepo + "@" + repairOverlayBranch)

	handled, res := repairEdgesCall(t, f,
		`{"operation":"repair_edges","graph":"code","branch":"launch-fixes","execute":true}`)
	require.True(t, handled)
	assert.True(t, res.IsError, "branch with an empty name must be an error, not a silent whole-catalog sweep")
	assert.Contains(t, toolResultText(res), "requires name")
	assert.Empty(t, f.queryPlans, "the error short-circuits BEFORE any read")
	assert.Empty(t, f.mutations, "the error mutates nothing")
}
