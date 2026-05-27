// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// orphan_rules_cicd_test.go covers the four CI/CD orphan rules. The graph is
// served over the wire by the cloud fixture; CI/CD resources are added as
// NodeCICDResource nodes. The dead-workflow rule resolves the source node's
// resource_type from the in-memory node map, so the synthetic graph seeds the
// workflow_run node alongside the BELONGS_TO edge.

const cicdGraph = "github-myorg"

func TestCICDOrphanRulesRegistered(t *testing.T) {
	for _, rt := range []string{"workflow", "runner", "secret", "environment"} {
		_, ok := lookupOrphanRule(rt)
		assert.True(t, ok, "orphan rule for %q should be registered", rt)
	}
}

// --- Dead workflow rule ---

func TestDeadWorkflowRule_NoRuns(t *testing.T) {
	fx := newCloudFixture(t)
	wfID := "github:myorg/Workflow/myorg/api/ci.yml"
	fx.AddCICDResource(cicdGraph, wfID, "CI", "workflow", nil)

	orphan, conf, summary, err := deadWorkflowRule(context.Background(), fx, cicdGraph, fx.orphanGraphFor(t, cicdGraph), fx.nodeFor(t, cicdGraph, wfID))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, confidenceDeadWorkflow, conf, 0.001)
	assert.Contains(t, summary, "no recent runs")
}

func TestDeadWorkflowRule_HasRuns(t *testing.T) {
	fx := newCloudFixture(t)
	wfID := "github:myorg/Workflow/myorg/api/ci.yml"
	fx.AddCICDResource(cicdGraph, wfID, "CI", "workflow", nil)
	fx.AddCICDResource(cicdGraph, "github:myorg/WorkflowRun/myorg/api/100", "CI #1", "workflow_run", nil)
	fx.AddEdge(cicdGraph, "github:myorg/WorkflowRun/myorg/api/100", wfID, kgtypes.EdgeBelongsTo)

	orphan, _, _, err := deadWorkflowRule(context.Background(), fx, cicdGraph, fx.orphanGraphFor(t, cicdGraph), fx.nodeFor(t, cicdGraph, wfID))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- Idle runner rule ---

func TestIdleRunnerRule_NoRuns(t *testing.T) {
	fx := newCloudFixture(t)
	runnerID := "github:myorg/Runner/1"
	fx.AddCICDResource(cicdGraph, runnerID, "runner-1", "runner", nil)

	orphan, conf, summary, err := idleRunnerRule(context.Background(), fx, cicdGraph, fx.orphanGraphFor(t, cicdGraph), fx.nodeFor(t, cicdGraph, runnerID))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, confidenceIdleRunner, conf, 0.001)
	assert.Contains(t, summary, "no workflow runs")
}

func TestIdleRunnerRule_HasRuns(t *testing.T) {
	fx := newCloudFixture(t)
	runnerID := "github:myorg/Runner/1"
	fx.AddCICDResource(cicdGraph, runnerID, "runner-1", "runner", nil)
	fx.AddCICDResource(cicdGraph, "github:myorg/WorkflowRun/myorg/api/100", "CI #1", "workflow_run", nil)
	fx.AddEdge(cicdGraph, "github:myorg/WorkflowRun/myorg/api/100", runnerID, kgtypes.EdgeRunsIn)

	orphan, _, _, err := idleRunnerRule(context.Background(), fx, cicdGraph, fx.orphanGraphFor(t, cicdGraph), fx.nodeFor(t, cicdGraph, runnerID))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- Unused secret rule ---

func TestUnusedSecretRule_NotReferenced(t *testing.T) {
	fx := newCloudFixture(t)
	secretID := "github:myorg/Secret/repo/API_KEY"
	fx.AddCICDResource(cicdGraph, secretID, "API_KEY", "secret", nil)

	orphan, conf, _, err := unusedSecretRule(context.Background(), fx, cicdGraph, fx.orphanGraphFor(t, cicdGraph), fx.nodeFor(t, cicdGraph, secretID))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, confidenceUnusedSecret, conf, 0.001)
}

func TestUnusedSecretRule_Referenced(t *testing.T) {
	fx := newCloudFixture(t)
	secretID := "github:myorg/Secret/repo/API_KEY"
	fx.AddCICDResource(cicdGraph, secretID, "API_KEY", "secret", nil)
	fx.AddCICDResource(cicdGraph, "github:myorg/Workflow/myorg/api/ci.yml", "CI", "workflow", nil)
	fx.AddEdge(cicdGraph, "github:myorg/Workflow/myorg/api/ci.yml", secretID, kgtypes.EdgeUsesSecret)

	orphan, _, _, err := unusedSecretRule(context.Background(), fx, cicdGraph, fx.orphanGraphFor(t, cicdGraph), fx.nodeFor(t, cicdGraph, secretID))
	require.NoError(t, err)
	assert.False(t, orphan)
}

// --- Unused environment rule ---

func TestUnusedEnvironmentRule_NoDeploys(t *testing.T) {
	fx := newCloudFixture(t)
	envID := "github:myorg/Environment/myorg/api/production"
	fx.AddCICDResource(cicdGraph, envID, "production", "environment", nil)

	orphan, conf, _, err := unusedEnvironmentRule(context.Background(), fx, cicdGraph, fx.orphanGraphFor(t, cicdGraph), fx.nodeFor(t, cicdGraph, envID))
	require.NoError(t, err)
	assert.True(t, orphan)
	assert.InDelta(t, confidenceUnusedEnvironment, conf, 0.001)
}

func TestUnusedEnvironmentRule_HasDeploys(t *testing.T) {
	fx := newCloudFixture(t)
	envID := "github:myorg/Environment/myorg/api/production"
	fx.AddCICDResource(cicdGraph, envID, "production", "environment", nil)
	fx.AddCICDResource(cicdGraph, "github:myorg/Deployment/myorg/api/1", "deploy", "deployment", nil)
	fx.AddEdge(cicdGraph, "github:myorg/Deployment/myorg/api/1", envID, kgtypes.EdgeDeploysTo)

	orphan, _, _, err := unusedEnvironmentRule(context.Background(), fx, cicdGraph, fx.orphanGraphFor(t, cicdGraph), fx.nodeFor(t, cicdGraph, envID))
	require.NoError(t, err)
	assert.False(t, orphan)
}
