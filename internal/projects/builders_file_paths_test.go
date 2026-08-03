// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestBuildPlanGraph_FilePathsEmitNoStepFileEdge pins the file-link phantom fix:
// a step carrying FilePaths must emit NO step→file edge, and the paths must
// still land as file_paths metadata.
//
// The removed emission targeted ToID "file:"+path, an identity that exists
// nowhere else in the tree — no such node is ever created, in any graph. The
// bare path would not have worked either: file nodes live in the CODE graph
// while create_plan writes to KNOWLEDGE, and write-id resolution only consults
// the graph being written. So the edge never once produced a working link.
//
// It failed in two different ways depending on path LENGTH, which is why it
// read as intermittent: a long path silently persisted a dangling edge, while a
// short one aborted the ENTIRE create_plan batch with a not-found error and zero
// nodes written. Both lengths are exercised below so a re-add cannot pass by
// only reintroducing one branch.
//
// Genuine cross-graph step→file linking goes through the linkage graph, which
// resolves correctly and is the supported mechanism.
func TestBuildPlanGraph_FilePathsEmitNoStepFileEdge(t *testing.T) {
	const (
		shortPath = "a.go"                                        // < 32 chars: aborted the whole batch
		longPath  = "cmd/knowledge/internal/projects/builders.go" // >= 32 chars: dangling edge
	)
	require.Less(t, len(shortPath), 32, "the short-path case must stay under the resolution cutoff")
	require.GreaterOrEqual(t, len(longPath), 32, "the long-path case must stay at or above the cutoff")

	plan := PlanArgs{
		Name:             "p-with-file-paths",
		Goal:             "test",
		NoPatternsReason: "test",
		Phases: []PhaseArgs{{
			Name:     "phase-1",
			Overview: "only phase",
			Steps: []StepArgs{{
				Name:        "step-1",
				Description: "only step",
				FilePaths:   shortPath + "," + longPath,
			}},
		}},
	}

	nodes, edges := BuildPlanGraph(plan, nil, nil)

	for _, e := range edges {
		assert.NotEqualf(t, kgtypes.EdgeKGImplements, e.Type,
			"no implements edge may be emitted for FilePaths — the step→file link is deliberately absent (ToID %q)", e.ToID)
		assert.Falsef(t, strings.HasPrefix(e.ToID, "file:"),
			"edge targets the phantom file: identity %q — no such node is ever created", e.ToID)
		for _, fp := range []string{shortPath, longPath} {
			assert.NotEqualf(t, fp, e.ToID,
				"edge targets the bare path %q — file nodes live in the code graph, not the one create_plan writes", fp)
		}
	}

	// The paths are NOT lost: dropping the edge must not drop the data.
	var stepNode *knowledgev1.Node
	for _, n := range nodes {
		if n.GetType() == string(kgtypes.NodeStep) {
			stepNode = n
			break
		}
	}
	require.NotNil(t, stepNode, "the plan graph must contain the step node")
	assert.Equal(t, shortPath+","+longPath, kgtypes.Value(stepNode, "file_paths"),
		"FilePaths must still persist as file_paths metadata on the step")
}
