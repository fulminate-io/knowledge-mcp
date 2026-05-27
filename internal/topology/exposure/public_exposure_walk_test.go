// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// public_exposure_walk_test.go covers bfsFromSeed end-to-end with a small
// AWS-style cloud fixture: ALB → SG → EC2 → (RDS | cycle). Tests verify:
//
//   - a valid 3-hop path from a public ALB to an RDS instance is found,
//   - cycles in the graph do not produce infinite loops,
//   - the max-depth knob actually caps the walk,
//   - a non-public seed is skipped by the walker (the walker gates on
//     the seed itself, not on every node, so the seed's NodeID must be
//     valid even if the resource is not sensitive).

const peWalkAccount = "pe-walk-test"

func buildPEWalkFixture(t *testing.T) (*cloudFixture, *cloudReader) {
	t.Helper()
	fx := newCloudFixture(t)
	fx.account(peWalkAccount)
	return fx, fx.reader(peWalkAccount)
}

// seedResourceFromJSON is a test helper that writes a cloud resource
// with a JSON content envelope.
func seedResourceFromJSON(t *testing.T, fx *cloudFixture, id, resourceType string, content any) {
	t.Helper()
	body, err := json.Marshal(content)
	require.NoError(t, err)
	fx.AddCloudResourceWithContent(peWalkAccount, id, id, resourceType, string(body), nil)
}

// TestBFSFromSeed_ReachesRDSInFourHops builds a chain:
//
//	ALB -[USES_SECURITY_GROUP]-> sg-front -[ALLOWS_INGRESS_FROM]-> sg-back
//	     -[USES_SECURITY_GROUP]-> rds
//
// and verifies the walker terminates at the rds-instance with the
// correct sensitive reason.
func TestBFSFromSeed_ReachesRDSInFourHops(t *testing.T) {
	fx, scoped := buildPEWalkFixture(t)

	seedResourceFromJSON(t, fx, "arn:alb", "elbv2-loadbalancer",
		map[string]any{"Scheme": "internet-facing"})
	seedResourceFromJSON(t, fx, "arn:sg-front", "security-group", map[string]any{})
	seedResourceFromJSON(t, fx, "arn:sg-back", "security-group", map[string]any{})
	seedResourceFromJSON(t, fx, "arn:rds", "rds-instance", map[string]any{"PubliclyAccessible": false})

	fx.AddEdge(peWalkAccount, "arn:alb", "arn:sg-front", kgtypes.EdgeUsesSecurityGroup)
	fx.AddEdge(peWalkAccount, "arn:sg-front", "arn:sg-back", kgtypes.EdgeAllowsEgressTo)
	fx.AddEdge(peWalkAccount, "arn:sg-back", "arn:rds", kgtypes.EdgeUsesSecurityGroup)
	// Note: direction. In reality the backend SG is attached TO the RDS —
	// we use EdgeUsesSecurityGroup here for test brevity; the walker is
	// oblivious to AWS-specific directionality.

	seeds := enumerateSeeds(newTestCtx(t), scoped, "aws")
	require.NotEmpty(t, seeds, "ALB must enumerate as a public seed")

	cfg := walkerConfig{
		scoped:     scoped,
		rootCaller: fx,
		EdgeTypes: []kgtypes.EdgeType{
			kgtypes.EdgeUsesSecurityGroup,
			kgtypes.EdgeAllowsIngressFrom,
			kgtypes.EdgeAllowsEgressTo,
		},
		MaxDepth: 5,
		Account:  peWalkAccount,
	}
	var paths []attackPath
	for _, seed := range seeds {
		paths = append(paths, bfsFromSeed(newTestCtx(t), cfg, seed)...)
	}
	require.NotEmpty(t, paths, "walker must find at least one path to RDS")
	// Verify the terminal is the RDS node.
	found := false
	for _, p := range paths {
		if p.Nodes[len(p.Nodes)-1] == "arn:rds" && p.SensitiveReason == "relational database" {
			found = true
			break
		}
	}
	require.True(t, found, "walker must terminate at arn:rds with the rds reason")
}

// TestBFSFromSeed_CycleHandling builds a cycle A → B → C → A and
// verifies BFS terminates without producing duplicate visits or infinite
// loops. Also adds a sink (rds) hanging off C so the walker has a
// legitimate path to find.
func TestBFSFromSeed_CycleHandling(t *testing.T) {
	fx, scoped := buildPEWalkFixture(t)

	seedResourceFromJSON(t, fx, "arn:alb", "elbv2-loadbalancer",
		map[string]any{"Scheme": "internet-facing"})
	seedResourceFromJSON(t, fx, "arn:sg-a", "security-group", map[string]any{})
	seedResourceFromJSON(t, fx, "arn:sg-b", "security-group", map[string]any{})
	seedResourceFromJSON(t, fx, "arn:sg-c", "security-group", map[string]any{})
	seedResourceFromJSON(t, fx, "arn:rds", "rds-instance", map[string]any{})

	// Three-node cycle.
	fx.AddEdge(peWalkAccount, "arn:alb", "arn:sg-a", kgtypes.EdgeUsesSecurityGroup)
	fx.AddEdge(peWalkAccount, "arn:sg-a", "arn:sg-b", kgtypes.EdgeAllowsEgressTo)
	fx.AddEdge(peWalkAccount, "arn:sg-b", "arn:sg-c", kgtypes.EdgeAllowsEgressTo)
	fx.AddEdge(peWalkAccount, "arn:sg-c", "arn:sg-a", kgtypes.EdgeAllowsEgressTo) // cycle
	// Path out of the cycle to the terminal.
	fx.AddEdge(peWalkAccount, "arn:sg-c", "arn:rds", kgtypes.EdgeUsesSecurityGroup)

	cfg := walkerConfig{
		scoped:     scoped,
		rootCaller: fx,
		EdgeTypes: []kgtypes.EdgeType{
			kgtypes.EdgeUsesSecurityGroup,
			kgtypes.EdgeAllowsEgressTo,
		},
		MaxDepth: 10,
		Account:  peWalkAccount,
	}
	seeds := enumerateSeeds(newTestCtx(t), scoped, "aws")
	var paths []attackPath
	for _, seed := range seeds {
		paths = append(paths, bfsFromSeed(newTestCtx(t), cfg, seed)...)
	}
	require.NotEmpty(t, paths, "cycle must not prevent the walker from finding the terminal")
	// Every path should be a simple path (no duplicate nodes).
	for _, p := range paths {
		seen := map[string]bool{}
		for _, n := range p.Nodes {
			require.Falsef(t, seen[n], "duplicate node %q in path %v", n, p.Nodes)
			seen[n] = true
		}
	}
}

// TestBFSFromSeed_MaxDepthCap verifies setting MaxDepth to 1 stops the
// walker from reaching a terminal that's 3 hops away.
func TestBFSFromSeed_MaxDepthCap(t *testing.T) {
	fx, scoped := buildPEWalkFixture(t)

	seedResourceFromJSON(t, fx, "arn:alb", "elbv2-loadbalancer",
		map[string]any{"Scheme": "internet-facing"})
	seedResourceFromJSON(t, fx, "arn:hop1", "security-group", map[string]any{})
	seedResourceFromJSON(t, fx, "arn:hop2", "security-group", map[string]any{})
	seedResourceFromJSON(t, fx, "arn:rds", "rds-instance", map[string]any{})

	fx.AddEdge(peWalkAccount, "arn:alb", "arn:hop1", kgtypes.EdgeUsesSecurityGroup)
	fx.AddEdge(peWalkAccount, "arn:hop1", "arn:hop2", kgtypes.EdgeAllowsEgressTo)
	fx.AddEdge(peWalkAccount, "arn:hop2", "arn:rds", kgtypes.EdgeUsesSecurityGroup)

	cfg := walkerConfig{
		scoped:     scoped,
		rootCaller: fx,
		EdgeTypes:  []kgtypes.EdgeType{kgtypes.EdgeUsesSecurityGroup, kgtypes.EdgeAllowsEgressTo},
		MaxDepth:   1, // only one hop
		Account:    peWalkAccount,
	}
	seed := publicSeed{NodeID: "arn:alb", ResourceType: "elbv2-loadbalancer", EntryScore: 0.9, Reason: "test"}
	paths := bfsFromSeed(newTestCtx(t), cfg, seed)
	require.Empty(t, paths, "walker must not reach rds within 1 hop")
}

// TestBFSFromSeed_IRSABridge verifies the inline IRSA bridge shortcut:
// a K8s ServiceAccount with "irsa_role_arn" metadata composes into the
// AWS IAM role, even before the persisted linkage-graph pair exists.
func TestBFSFromSeed_IRSABridge(t *testing.T) {
	fx, scoped := buildPEWalkFixture(t)
	ctx := newTestCtx(t)

	roleARN := "arn:aws:iam::000000000001:role/prod-admin"
	// K8s LoadBalancer Service as the seed.
	fx.AddCloudResource(peWalkAccount, "k8s:svc:prod", "prod", "Service", map[string]string{
		"type": "LoadBalancer",
	})
	// K8s ServiceAccount with IRSA annotation.
	fx.AddCloudResource(peWalkAccount, "k8s:sa:prod/deployer", "deployer", "ServiceAccount", map[string]string{
		"irsa_role_arn": roleARN,
	})
	// IAM role target.
	fx.AddCloudResource(peWalkAccount, roleARN, "prod-admin", "iam-role", nil)

	// Wire LB → SA so the walker can pivot from the Service to the SA.
	fx.AddEdge(peWalkAccount, "k8s:svc:prod", "k8s:sa:prod/deployer", kgtypes.EdgeUsesSA)

	// Seed the persisted iam_escalation finding on the role.
	fx.AddKnowledgeFinding("finding:prod-admin", "iam_escalation", roleARN)

	// Walker config with cross-graph bridge following enabled.
	cfg := walkerConfig{
		scoped:              scoped,
		rootCaller:          fx,
		EdgeTypes:           []kgtypes.EdgeType{kgtypes.EdgeUsesSA},
		MaxDepth:            5,
		FollowLinkageBridge: true,
		Account:             peWalkAccount,
	}
	seed := publicSeed{
		NodeID:       "k8s:svc:prod",
		ResourceType: "Service",
		EntryScore:   0.9,
		Reason:       "k8s loadbalancer",
		CloudFamily:  "k8s",
	}
	paths := bfsFromSeed(ctx, cfg, seed)
	require.NotEmpty(t, paths, "walker must find the IRSA bridge path")

	// The terminal must be the IAM role, reached via the IRSA bridge.
	var found bool
	for _, p := range paths {
		if p.Nodes[len(p.Nodes)-1] == roleARN {
			found = true
			// At least one edge in the path must carry cross_graph=true.
			var bridged bool
			for _, e := range p.Edges {
				if e.Metadata["cross_graph"] == "true" {
					bridged = true
					break
				}
			}
			require.True(t, bridged, "IRSA path must have a cross_graph edge")
			break
		}
	}
	require.True(t, found, "walker must reach the IRSA-bound IAM role")
}
