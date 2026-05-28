// SPDX-License-Identifier: Apache-2.0

package cloud

import (
	"context"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

const confidenceGCPGroup = 0.9

// gcpGroupRule reports a Cloud Identity group as orphaned when it has no
// outbound EdgeHasMember edges (no members). A group with no members is
// effectively unused from an access-control perspective. Confidence is 0.9
// because some groups may be intentionally empty during setup or migration.
func gcpGroupRule(
	_ context.Context,
	_ foundation.GraphCaller,
	_ string,
	graph *orphanGraph,
	node *knowledgev1.Node,
) (bool, float64, string, error) {
	if graph.edges.hasOutgoing(node.Id, kgtypes.EdgeHasMember) {
		return false, confidenceGCPGroup, "", nil
	}
	return true, confidenceGCPGroup,
		fmt.Sprintf("Cloud Identity group %s has no members.", displayName(node)),
		nil
}

func init() {
	registerOrphanRule("gcp:cloudidentity:group", gcpGroupRule)
}
