// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// orphan_rules_cicd.go registers CI/CD orphan rules:
//
//   - workflow       → dead workflow: no BELONGS_TO inbound from workflow_run  (conf 0.8)
//   - runner         → idle runner: no inbound RUNS_IN edges                   (conf 0.9)
//   - secret         → unused secret: no inbound USES_SECRET edges             (conf 0.7)
//   - environment    → unused environment: no inbound DEPLOYS_TO edges         (conf 0.8)

const (
	confidenceDeadWorkflow      = 0.8
	confidenceIdleRunner        = 0.9
	confidenceUnusedSecret      = 0.7
	confidenceUnusedEnvironment = 0.8
)

// deadWorkflowRule reports a workflow as dead when it has no associated
// workflow runs. Runs are linked to workflows via BELONGS_TO, so we check
// for inbound BELONGS_TO edges whose source is a workflow_run node. The
// source resource_type is resolved from the in-memory node map rather than
// a per-edge by-id query.
func deadWorkflowRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	for _, fromID := range graph.edges.incomingFrom(node.Id, kgtypes.EdgeBelongsTo) {
		if graph.resourceType(fromID) == "workflow_run" {
			return false, confidenceDeadWorkflow, "", nil
		}
	}
	return true, confidenceDeadWorkflow,
		fmt.Sprintf("Workflow %s has no recent runs.", displayName(node)),
		nil
}

// idleRunnerRule reports a runner as idle when no workflow run references it
// via RUNS_IN. An idle runner is one that has never executed any collected
// workflow runs.
func idleRunnerRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeRunsIn) {
		return false, confidenceIdleRunner, "", nil
	}
	return true, confidenceIdleRunner,
		fmt.Sprintf("Runner %s has no workflow runs.", displayName(node)),
		nil
}

// unusedSecretRule reports a secret as unused when no workflow references it
// via USES_SECRET. The confidence is lower (0.7) because secrets might be
// referenced by workflows we haven't parsed yet (e.g., reusable workflows
// in other repos).
func unusedSecretRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeUsesSecret) {
		return false, confidenceUnusedSecret, "", nil
	}
	return true, confidenceUnusedSecret,
		fmt.Sprintf("Secret %s is not referenced by any collected workflow.", displayName(node)),
		nil
}

// unusedEnvironmentRule reports an environment as unused when no deployment
// or workflow targets it via DEPLOYS_TO.
func unusedEnvironmentRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasIncoming(node.Id, kgtypes.EdgeDeploysTo) {
		return false, confidenceUnusedEnvironment, "", nil
	}
	return true, confidenceUnusedEnvironment,
		fmt.Sprintf("Environment %s has no deployments.", displayName(node)),
		nil
}

func init() {
	registerOrphanRule("workflow", deadWorkflowRule)
	registerOrphanRule("runner", idleRunnerRule)
	registerOrphanRule("secret", unusedSecretRule)
	registerOrphanRule("environment", unusedEnvironmentRule)
}
