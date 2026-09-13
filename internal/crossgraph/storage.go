// SPDX-License-Identifier: Apache-2.0

package crossgraph

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/engine"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/projects/render"
)

// resolveStorageLink resolves both endpoints before materializing a local proxy.
// A cloud destination is never allowed to contain a device-local reference.
func resolveStorageLink(ctx context.Context, ex render.Executor, req LinkRequest) (bool, kgtools.ToolResult, error) {
	from, fq, fe := graphclient.ParseReference(req.From)
	to, tq, te := graphclient.ParseReference(req.To)
	if !fq && !tq {
		return false, kgtools.ToolResult{}, nil
	}
	fail := func(err error) (bool, kgtools.ToolResult, error) { return true, kgtools.ErrorResult(err.Error()), err }
	if fe != nil {
		return fail(fe)
	}
	if te != nil {
		return fail(te)
	}
	d, bound := graphclient.StorageDestination(ctx)
	if !fq {
		if !bound {
			return fail(fmt.Errorf("unqualified source requires a bound storage destination"))
		}
		from.Destination, from.Graph = d, req.TargetGraph
		if from.Graph == "" {
			from.Graph = "knowledge"
		}
	}
	if !tq {
		to.Destination, to.Graph, to.Repo, to.Name, to.Language, to.Branch = from.Destination, from.Graph, from.Repo, from.Name, from.Language, from.Branch
	}
	if err := validateStorageLinkDirection(from.Destination, to.Destination); err != nil {
		return fail(err)
	}
	var validated int64
	if req.LastValidated != "" {
		v, err := time.Parse(time.RFC3339, req.LastValidated)
		if err != nil {
			return fail(err)
		}
		validated = v.UnixNano()
	}
	ctx = graphclient.WithDestination(ctx, from.Destination)
	resolved, err := engine.ResolveEdgeTypeDeclaration(ctx, req.Stats, from.Selector(), []string{req.Relationship})
	if err != nil {
		return fail(err)
	}
	nodes, err := readStorageLinkEndpoints(ctx, ex, from, to)
	if err != nil {
		return fail(err)
	}
	toID := to.ID
	if from.Destination != to.Destination || !proto.Equal(from.Selector(), to.Selector()) {
		encoded, err := to.Encode()
		if err != nil {
			return fail(err)
		}
		toID = fmt.Sprintf("storage-proxy:%x", sha256.Sum256([]byte(encoded)))
		_, err = ex.Execute(ctx, &knowledgev1.ExecuteRequest{Target: from.Selector(), Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
			Kind:       knowledgev1.MutationPlan_MUTATION_KIND_UPSERT,
			NodeBodies: []*knowledgev1.NodeBody{{Id: toID, Type: "proxy", Name: nodes[1].GetSymbolName(), Description: nodes[1].GetDescription(), Source: encoded, Metadata: map[string]string{"storage_reference": encoded, "foreign_graph": to.Graph}}},
		}}})
		if err != nil {
			return fail(err)
		}
	}
	_, err = ex.Execute(ctx, &knowledgev1.ExecuteRequest{Target: from.Selector(), Plan: &knowledgev1.ExecuteRequest_Mutation{Mutation: &knowledgev1.MutationPlan{
		Kind: knowledgev1.MutationPlan_MUTATION_KIND_LINK, Selection: &knowledgev1.Selection{Ids: []string{from.ID}},
		EdgeSpec: &knowledgev1.EdgeSpec{ToId: toID, Relationship: resolved.Types[0], Forward: true, Weight: req.Weight, Confidence: req.Confidence, Method: req.Method, Evidence: req.Evidence, LastValidated: validated},
	}}})
	if err != nil {
		return fail(err)
	}
	return true, kgtools.TextResult(fmt.Sprintf("Linked %s -[%s]-> %s", req.From, resolved.Types[0], req.To)), nil
}

func readStorageLinkEndpoints(ctx context.Context, ex render.Executor, from, to graphclient.Reference) ([2]*knowledgev1.Node, error) {
	var nodes [2]*knowledgev1.Node
	destinations := [2]graphclient.Destination{from.Destination, to.Destination}
	for i, ref := range []graphclient.Reference{from, to} {
		response, err := ex.Execute(graphclient.WithDestination(ctx, ref.Destination), &knowledgev1.ExecuteRequest{Target: ref.Selector(), Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: ref.ID}}})
		if err != nil {
			return nodes, err
		}
		if len(response.GetNodes()) != 1 {
			return nodes, fmt.Errorf("reference endpoint %q was not found", ref.ID)
		}
		nodes[i] = response.GetNodes()[0]
		// Hydrated proxy bodies retain their durable storage reference. Placement
		// alone is not the logical endpoint identity used to validate direction.
		if encoded := nodes[i].GetMetadata()["storage_reference"]; encoded != "" {
			logical, qualified, err := graphclient.ParseReference(encoded)
			if err != nil {
				return nodes, err
			}
			if !qualified {
				return nodes, fmt.Errorf("reference endpoint %q has an invalid storage reference", ref.ID)
			}
			destinations[i] = logical.Destination
		}
	}
	return nodes, validateStorageLinkDirection(destinations[0], destinations[1])
}

// validateStorageLinkDirection applies to both literal and resolved endpoints.
func validateStorageLinkDirection(from, to graphclient.Destination) error {
	if from.Storage == "cloud" && to.Storage == "local" {
		return fmt.Errorf("cloud-to-local relationships are not allowed")
	}
	if from.Storage == "cloud" && from.AccountID != to.AccountID {
		return fmt.Errorf("cross-account relationships are not allowed")
	}
	return nil
}
