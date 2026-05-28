// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/logs/cloudresolver"
)

// FetchCloudSubgraph asks the server for an in-memory slice of cloud
// graphs the client uses to drive logs.CloudResolver and
// logs.DependencyChecker locally during collect(type=logs, ...).
//
// graphNames may be nil/empty (server returns every loaded cloud
// graph). typePrefixes is an optional resource_type filter (e.g.
// ["Service", "Deployment", "k8s:Service", "ecs:service", ...]) — empty
// means "return everything".
func (s *UploadSink) FetchCloudSubgraph(
	ctx context.Context,
	graphNames []string,
	typePrefixes []string,
) (*cloudresolver.CloudSubgraph, error) {
	req := connect.NewRequest(&knowledgev1.FetchCloudSubgraphRequest{
		GraphNames:   graphNames,
		TypePrefixes: typePrefixes,
	})
	resp, err := s.client.FetchCloudSubgraph(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("remote sink: FetchCloudSubgraph: %w", err)
	}
	slices := make([]cloudresolver.GraphSlice, 0, len(resp.Msg.Slices))
	for _, slice := range resp.Msg.Slices {
		// NodesJson is the deliberately-retained collect-path blob carrier
		// (CloudSubgraphSlice, ingest.proto:136) — NOT an Execute-response
		// node blob. The server marshals its node slice with an anonymous
		// knowledgev1.Node embed (json:"-" hints), making the bytes
		// byte-identical to a []*knowledgev1.Node round-trip.
		var nodes []*knowledgev1.Node
		if len(slice.NodesJson) > 0 {
			if err := json.Unmarshal(slice.NodesJson, &nodes); err != nil {
				return nil, fmt.Errorf("remote sink: unmarshal nodes for %q: %w", slice.GraphName, err)
			}
		}
		slices = append(slices, cloudresolver.GraphSlice{
			Name:  slice.GraphName,
			Nodes: nodes,
			Edges: edgesFromProto(slice.GetEdges()),
		})
	}
	return cloudresolver.NewCloudSubgraph(slices), nil
}
