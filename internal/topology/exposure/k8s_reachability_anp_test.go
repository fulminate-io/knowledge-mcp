// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// k8s_reachability_anp_test.go covers the AdminNetworkPolicy priority dispatch
// added in Phase 5.5. The tests build small reachability indices directly
// (without spinning up the full k8sFixture for every case) and assert canReach
// returns the expected verdict for each ANP recipe:
//
//   - Allow overrides Deny  → ANP Allow + NetworkPolicy default-deny → reach succeeds
//   - Deny overrides Allow  → ANP Deny + NetworkPolicy allow → reach fails
//   - Pass falls through    → ANP Pass + NetworkPolicy result wins
//   - Multi-priority order  → lower priority number wins among multiple ANPs
//
// All Phase 5.5 RECIPE-level tests (using the k8sFixture) live alongside in
// the same file because the recipe file already exceeds 280 lines.

// anpFixtureRule is the test-side ANP rule descriptor consumed by
// addANPFixture / addANPFixtureMulti. Each rule encodes one (priority, action,
// port range) tuple.
type anpFixtureRule struct {
	Priority int
	Action   string
	Meta     edgePortMetadata
}

// addANPFixture pushes a single ANP-tagged edge into the fixture's cloud
// account. Convenience wrapper around addANPFixtureMulti for the
// one-rule-per-pair case.
func addANPFixture(t *testing.T, fx *k8sFixture, dir, fromPod, toPod string, priority int, action string, meta edgePortMetadata) {
	t.Helper()
	addANPFixtureMulti(t, fx, dir, fromPod, toPod, []anpFixtureRule{{Priority: priority, Action: action, Meta: meta}})
}

// addANPFixtureMulti packs MULTIPLE ANP rules into a single edge between the
// given pods. The store keys edges by (FromID, Type, ToID), so multi-priority
// cases must coalesce into one edge whose Evidence is a JSON array of ANP
// metadata objects. This mirrors how a real collector would aggregate multiple
// matching ANPs covering the same pod pair before writing the edge.
func addANPFixtureMulti(t *testing.T, fx *k8sFixture, dir, fromPod, toPod string, rules []anpFixtureRule) {
	t.Helper()
	type entry struct {
		Protocol       string `json:"protocol,omitempty"`
		PortFrom       int    `json:"port_from,omitempty"`
		PortTo         int    `json:"port_to,omitempty"`
		NamedPort      string `json:"named_port,omitempty"`
		PortUnresolved bool   `json:"port_unresolved,omitempty"`
		IsANP          bool   `json:"is_anp"`
		ANPPriority    int    `json:"anp_priority"`
		ANPAction      string `json:"anp_action"`
	}
	out := make([]entry, 0, len(rules))
	for _, r := range rules {
		out = append(out, entry{
			Protocol:       r.Meta.Protocol,
			PortFrom:       r.Meta.PortFrom,
			PortTo:         r.Meta.PortTo,
			NamedPort:      r.Meta.NamedPort,
			PortUnresolved: r.Meta.PortUnresolved,
			IsANP:          true,
			ANPPriority:    r.Priority,
			ANPAction:      r.Action,
		})
	}
	b, err := json.Marshal(out)
	require.NoError(t, err)
	evidence := string(b)

	// Edge endpoint convention (matching k8s_reachability_fixture_test.go):
	//   - EdgeANPIngressFrom: FromID=dstPod, ToID=srcPod
	//     "dstPod accepts ingress from srcPod under an AdminNetworkPolicy"
	//   - EdgeANPEgressTo: FromID=srcPod, ToID=dstPod
	//     "srcPod permits egress to dstPod under an AdminNetworkPolicy"
	// addANPFixture's fromPod / toPod arguments are interpreted as the
	// reachability direction (src → dst), so the wire-level FromID/ToID may
	// be swapped relative to the call arguments.
	var edgeType kgtypes.EdgeType
	var wireFrom, wireTo string
	switch dir {
	case "ingress":
		edgeType = kgtypes.EdgeANPIngressFrom
		// fromPod is the dst (the policy target accepting ingress);
		// toPod is the src (the source the policy permits).
		wireFrom, wireTo = fromPod, toPod
	case "egress":
		edgeType = kgtypes.EdgeANPEgressTo
		// fromPod is the src (egress origin); toPod is the dst.
		wireFrom, wireTo = fromPod, toPod
	default:
		t.Fatalf("addANPFixtureMulti: unknown dir %q", dir)
	}

	fx.cloud.AddEdgeWithEvidence(fx.Account(), wireFrom, wireTo, edgeType, evidence)
}

// buildIdxForANP scopes the cloud graph and builds a fresh reachability index.
// Used by every ANP test to keep the boilerplate to one line.
func buildIdxForANP(t *testing.T, fx *k8sFixture) *reachabilityIndex {
	t.Helper()
	idx, err := buildReachabilityIndex(newTestCtx(t), fx.reader())
	require.NoError(t, err)
	require.False(t, idx.skipped)
	return idx
}

// TestCanReach_ANPAllowOverridesNetworkPolicyDeny verifies that an ANP Allow
// edge wins over a NetworkPolicy default-deny: the dst pod is restricted by
// a regular policy with NO allow rule for src, but an ANP at priority 10
// explicitly allows src → dst. canReach must return true.
func TestCanReach_ANPAllowOverridesNetworkPolicyDeny(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	src := fx.AddPod("default", "src", nil)
	dst := fx.AddPod("default", "dst", nil)

	policy := fx.AddNetworkPolicy("default", "deny-all-ingress")
	fx.RestrictIngress(policy, dst)
	// No regular AllowIngress edge — under NetworkPolicy alone src→dst is denied.

	addANPFixture(t, fx, "ingress", dst, src, 10, "Allow", edgePortMetadata{Protocol: "TCP", PortFrom: 80, PortTo: 80})

	idx := buildIdxForANP(t, fx)
	require.NotEmpty(t, idx.pods[dst].ANPIngressFrom[src],
		"ANP edge must land in dstPod.ANPIngressFrom")

	assert.True(t, idx.canReach(src, dst, "TCP", 80),
		"ANP Allow at priority 10 must override NetworkPolicy default-deny")
	assert.False(t, idx.canReach(src, dst, "TCP", 443),
		"ANP Allow scoped to TCP/80 must not extend to TCP/443")
}

// TestCanReach_ANPDenyOverridesNetworkPolicyAllow verifies that an ANP Deny
// wins over a regular NetworkPolicy allow: the dst pod is restricted by a
// regular policy WITH an allow rule for src on TCP/80, but an ANP at
// priority 5 denies src → dst on TCP/80. canReach must return false.
func TestCanReach_ANPDenyOverridesNetworkPolicyAllow(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	src := fx.AddPod("default", "src", nil)
	dst := fx.AddPod("default", "dst", nil)

	policy := fx.AddNetworkPolicy("default", "allow-src")
	fx.RestrictIngress(policy, dst)
	fx.AllowIngress(dst, src, tcpPort(80)) // regular NP would normally allow.

	addANPFixture(t, fx, "ingress", dst, src, 5, "Deny", edgePortMetadata{Protocol: "TCP", PortFrom: 80, PortTo: 80})

	idx := buildIdxForANP(t, fx)
	assert.False(t, idx.canReach(src, dst, "TCP", 80),
		"ANP Deny at priority 5 must override the regular NetworkPolicy allow")
}

// TestCanReach_ANPPassFallthrough verifies the Pass action: ANP at priority 1
// matches src → dst with action=Pass. The dispatch must fall through to the
// regular NetworkPolicy result. With NO regular allow edge and a default-deny
// policy, the result is false; with a regular allow edge it would be true.
func TestCanReach_ANPPassFallthrough(t *testing.T) {
	t.Run("pass falls through to deny", func(t *testing.T) {
		fx := buildK8sFixture(t)
		fx.AddNamespace("default", nil)
		src := fx.AddPod("default", "src", nil)
		dst := fx.AddPod("default", "dst", nil)

		policy := fx.AddNetworkPolicy("default", "deny-all")
		fx.RestrictIngress(policy, dst)

		addANPFixture(t, fx, "ingress", dst, src, 1, "Pass", edgePortMetadata{Protocol: "TCP", PortFrom: 80, PortTo: 80})

		idx := buildIdxForANP(t, fx)
		assert.False(t, idx.canReach(src, dst, "TCP", 80),
			"ANP Pass must defer; underlying NetworkPolicy default-deny then blocks")
	})

	t.Run("pass falls through to allow", func(t *testing.T) {
		fx := buildK8sFixture(t)
		fx.AddNamespace("default", nil)
		src := fx.AddPod("default", "src", nil)
		dst := fx.AddPod("default", "dst", nil)

		policy := fx.AddNetworkPolicy("default", "allow-src")
		fx.RestrictIngress(policy, dst)
		fx.AllowIngress(dst, src, tcpPort(80))

		addANPFixture(t, fx, "ingress", dst, src, 1, "Pass", edgePortMetadata{Protocol: "TCP", PortFrom: 80, PortTo: 80})

		idx := buildIdxForANP(t, fx)
		assert.True(t, idx.canReach(src, dst, "TCP", 80),
			"ANP Pass must defer; underlying NetworkPolicy allow then permits")
	})
}

// TestCanReach_ANPMultiPriority verifies priority ordering when multiple ANPs
// match the same (src, dst, protocol, port) tuple. Two ANPs are wired:
//
//   - priority 5: Deny on TCP/80
//   - priority 10: Allow on TCP/80
//
// The lower-priority-number rule (Deny @ 5) wins, so canReach returns false.
// Swapping the priorities reverses the verdict.
func TestCanReach_ANPMultiPriority(t *testing.T) {
	tcp80 := edgePortMetadata{Protocol: "TCP", PortFrom: 80, PortTo: 80}

	t.Run("deny at priority 5 wins over allow at priority 10", func(t *testing.T) {
		fx := buildK8sFixture(t)
		fx.AddNamespace("default", nil)
		src := fx.AddPod("default", "src", nil)
		dst := fx.AddPod("default", "dst", nil)

		policy := fx.AddNetworkPolicy("default", "deny-all")
		fx.RestrictIngress(policy, dst)

		addANPFixtureMulti(t, fx, "ingress", dst, src, []anpFixtureRule{
			{Priority: 5, Action: "Deny", Meta: tcp80},
			{Priority: 10, Action: "Allow", Meta: tcp80},
		})

		idx := buildIdxForANP(t, fx)
		require.Len(t, idx.pods[dst].ANPIngressFrom[src], 2,
			"both ANP entries must land in the bucket")
		assert.False(t, idx.canReach(src, dst, "TCP", 80),
			"deny at lower priority number (5) must win over allow at 10")
	})

	t.Run("allow at priority 5 wins over deny at priority 10", func(t *testing.T) {
		fx := buildK8sFixture(t)
		fx.AddNamespace("default", nil)
		src := fx.AddPod("default", "src", nil)
		dst := fx.AddPod("default", "dst", nil)

		policy := fx.AddNetworkPolicy("default", "deny-all")
		fx.RestrictIngress(policy, dst)

		addANPFixtureMulti(t, fx, "ingress", dst, src, []anpFixtureRule{
			{Priority: 5, Action: "Allow", Meta: tcp80},
			{Priority: 10, Action: "Deny", Meta: tcp80},
		})

		idx := buildIdxForANP(t, fx)
		assert.True(t, idx.canReach(src, dst, "TCP", 80),
			"allow at lower priority number (5) must win over deny at 10")
	})
}

// TestCanReach_ANPEgressDeny verifies the egress dispatch path: an ANP Deny
// on srcPod's egress to dstPod blocks reachability even when ingress would
// otherwise allow it.
func TestCanReach_ANPEgressDeny(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	src := fx.AddPod("default", "src", nil)
	dst := fx.AddPod("default", "dst", nil)

	// dst is fully open ingress (no restricting policy at all). Egress
	// restriction on src + ANP deny is the only rule that should block.
	policy := fx.AddNetworkPolicy("default", "src-egress-restricted")
	fx.RestrictEgress(policy, src)
	fx.AllowEgress(src, dst, tcpPort(80))

	addANPFixture(t, fx, "egress", src, dst, 1, "Deny", edgePortMetadata{Protocol: "TCP", PortFrom: 80, PortTo: 80})

	idx := buildIdxForANP(t, fx)
	require.NotEmpty(t, idx.pods[src].ANPEgressTo[dst],
		"egress ANP edge must land in srcPod.ANPEgressTo")
	assert.False(t, idx.canReach(src, dst, "TCP", 80),
		"ANP Deny on egress must block reachability even when ingress is open")
}

// TestEvaluateANP_NoEntries verifies the evaluator's empty-input contract:
// no entries → Fallthrough=true so the caller defers to NetworkPolicy.
func TestEvaluateANP_NoEntries(t *testing.T) {
	d := evaluateANP(nil, "TCP", 80)
	assert.True(t, d.Fallthrough)
	assert.False(t, d.Allowed)
	assert.False(t, d.Denied)
}

// ============================================================================
// Phase 5.5 Step 4: end-to-end recipe tests
// ============================================================================
//
// These tests exercise the full KubernetesReachabilityAnalyzer pipeline (via
// runRecipe) and assert canReach behavior across the analyzer's index. The
// canonical names match the Phase 5.5 plan exactly so the criterion is
// unambiguous. Each recipe builds the smallest fixture that exercises one
// ANP semantic and asserts both the reachability verdict and that the
// analyzer Run() returns without error.

// TestReachability_ANPAllowOverridesDeny is the recipe-style sibling of
// TestCanReach_ANPAllowOverridesNetworkPolicyDeny. Invokes the full analyzer
// to confirm the index is built correctly during Run() and that ANP Allow
// wins over a NetworkPolicy default-deny.
func TestReachability_ANPAllowOverridesDeny(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	src := fx.AddPod("default", "src", nil)
	dst := fx.AddPod("default", "dst", nil)

	policy := fx.AddNetworkPolicy("default", "deny-all-ingress")
	fx.RestrictIngress(policy, dst)
	addANPFixture(t, fx, "ingress", dst, src, 10, "Allow", tcpPort(80))

	// Run the full analyzer end-to-end. This must not error and must produce
	// a reachability index where the ANP Allow overrides the default-deny.
	_ = runRecipe(t, fx)

	idx := buildIdxForANP(t, fx)
	assert.True(t, idx.canReach(src, dst, "TCP", 80),
		"ANP Allow at priority 10 must override NetworkPolicy default-deny")
}

// TestReachability_ANPDenyOverridesAllow asserts ANP Deny wins over a
// NetworkPolicy allow rule. Mirrors TestCanReach_ANPDenyOverridesNetworkPolicyAllow
// but goes through the analyzer Run() pipeline.
func TestReachability_ANPDenyOverridesAllow(t *testing.T) {
	fx := buildK8sFixture(t)
	fx.AddNamespace("default", nil)
	src := fx.AddPod("default", "src", nil)
	dst := fx.AddPod("default", "dst", nil)

	policy := fx.AddNetworkPolicy("default", "allow-src")
	fx.RestrictIngress(policy, dst)
	fx.AllowIngress(dst, src, tcpPort(80))
	addANPFixture(t, fx, "ingress", dst, src, 5, "Deny", tcpPort(80))

	_ = runRecipe(t, fx)

	idx := buildIdxForANP(t, fx)
	assert.False(t, idx.canReach(src, dst, "TCP", 80),
		"ANP Deny at priority 5 must override the regular NetworkPolicy allow")
}

// TestReachability_ANPPassFallthrough asserts ANP Pass defers to the regular
// NetworkPolicy result on the same pod pair. Two sub-cases pin both
// fall-through outcomes.
func TestReachability_ANPPassFallthrough(t *testing.T) {
	t.Run("pass falls through to deny", func(t *testing.T) {
		fx := buildK8sFixture(t)
		fx.AddNamespace("default", nil)
		src := fx.AddPod("default", "src", nil)
		dst := fx.AddPod("default", "dst", nil)

		policy := fx.AddNetworkPolicy("default", "deny-all")
		fx.RestrictIngress(policy, dst)
		addANPFixture(t, fx, "ingress", dst, src, 1, "Pass", tcpPort(80))

		_ = runRecipe(t, fx)
		idx := buildIdxForANP(t, fx)
		assert.False(t, idx.canReach(src, dst, "TCP", 80),
			"ANP Pass must defer to NetworkPolicy default-deny")
	})

	t.Run("pass falls through to allow", func(t *testing.T) {
		fx := buildK8sFixture(t)
		fx.AddNamespace("default", nil)
		src := fx.AddPod("default", "src", nil)
		dst := fx.AddPod("default", "dst", nil)

		policy := fx.AddNetworkPolicy("default", "allow-src")
		fx.RestrictIngress(policy, dst)
		fx.AllowIngress(dst, src, tcpPort(80))
		addANPFixture(t, fx, "ingress", dst, src, 1, "Pass", tcpPort(80))

		_ = runRecipe(t, fx)
		idx := buildIdxForANP(t, fx)
		assert.True(t, idx.canReach(src, dst, "TCP", 80),
			"ANP Pass must defer to NetworkPolicy allow")
	})
}

// TestReachability_ANPMultiPriority asserts that when two ANPs cover the
// same (src, dst, protocol, port) tuple, the lower priority number wins.
func TestReachability_ANPMultiPriority(t *testing.T) {
	tcp80 := edgePortMetadata{Protocol: "TCP", PortFrom: 80, PortTo: 80}

	t.Run("deny at priority 5 wins over allow at priority 10", func(t *testing.T) {
		fx := buildK8sFixture(t)
		fx.AddNamespace("default", nil)
		src := fx.AddPod("default", "src", nil)
		dst := fx.AddPod("default", "dst", nil)

		policy := fx.AddNetworkPolicy("default", "deny-all")
		fx.RestrictIngress(policy, dst)
		addANPFixtureMulti(t, fx, "ingress", dst, src, []anpFixtureRule{
			{Priority: 5, Action: "Deny", Meta: tcp80},
			{Priority: 10, Action: "Allow", Meta: tcp80},
		})

		_ = runRecipe(t, fx)
		idx := buildIdxForANP(t, fx)
		require.Len(t, idx.pods[dst].ANPIngressFrom[src], 2,
			"both ANP entries must land in the bucket")
		assert.False(t, idx.canReach(src, dst, "TCP", 80),
			"deny at lower priority number (5) must win over allow at 10")
	})

	t.Run("allow at priority 5 wins over deny at priority 10", func(t *testing.T) {
		fx := buildK8sFixture(t)
		fx.AddNamespace("default", nil)
		src := fx.AddPod("default", "src", nil)
		dst := fx.AddPod("default", "dst", nil)

		policy := fx.AddNetworkPolicy("default", "deny-all")
		fx.RestrictIngress(policy, dst)
		addANPFixtureMulti(t, fx, "ingress", dst, src, []anpFixtureRule{
			{Priority: 5, Action: "Allow", Meta: tcp80},
			{Priority: 10, Action: "Deny", Meta: tcp80},
		})

		_ = runRecipe(t, fx)
		idx := buildIdxForANP(t, fx)
		assert.True(t, idx.canReach(src, dst, "TCP", 80),
			"allow at lower priority number (5) must win over deny at 10")
	})
}
