// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

type storageProxyPathKey struct{}

// hydrateStorageProxies replaces persisted proxy snapshots with current target
// bodies. Reads are grouped by the reference reader, so duplicates share a fetch.
func (r *Router) hydrateStorageProxies(ctx context.Context, response *knowledgev1.ExecuteResponse) error {
	nodes := storageResponseSlots(response)
	targets := map[string][]**knowledgev1.Node{}
	for _, slot := range nodes {
		node := *slot
		if node == nil || node.Metadata["storage_reference"] == "" {
			continue
		}
		ref, qualified, err := ParseReference(node.Metadata["storage_reference"])
		if err != nil {
			return err
		}
		if !qualified {
			return fmt.Errorf("proxy %q has an invalid storage reference", node.Id)
		}
		if d, _ := StorageDestination(ctx); d.Storage == "cloud" && ref.Storage == "local" {
			return fmt.Errorf("cloud-to-local relationships are not allowed")
		}
		encoded, err := ref.Encode()
		if err != nil {
			return err
		}
		targets[encoded] = append(targets[encoded], slot)
	}
	if len(targets) == 0 {
		return nil
	}
	previous, _ := ctx.Value(storageProxyPathKey{}).(map[string]bool)
	path := make(map[string]bool)
	for key := range previous {
		path[key] = true
	}
	ids := make([]string, 0, len(targets))
	for key := range targets {
		if path[key] {
			return fmt.Errorf("cyclic storage proxy reference")
		}
		path[key] = true
		ids = append(ids, key)
	}
	resolved, err := r.Execute(context.WithValue(ctx, storageProxyPathKey{}, path), &knowledgev1.ExecuteRequest{Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: ids}}})
	if err != nil {
		return err
	}
	for _, node := range resolved.Nodes {
		for _, slot := range targets[node.Id] {
			proxy := *slot
			body := proto.CloneOf(node)
			body.Id = proxy.Id
			if body.Metadata == nil {
				body.Metadata = map[string]string{}
			}
			body.Metadata["storage_reference"] = proxy.Metadata["storage_reference"]
			*slot = body
		}
		delete(targets, node.Id)
	}
	if len(targets) != 0 {
		return fmt.Errorf("storage proxy target was not found")
	}
	return nil
}

type responseReferenceKey struct{}

// qualifyStorageResponse keeps neighbors and edge endpoints reusable alongside
// the requested node. Original spellings preserve version-one and plain IDs.
func qualifyStorageResponse(response *knowledgev1.ExecuteResponse, base Reference, original map[string]string) error {
	qualify := func(id *string) error {
		if *id == "" {
			return nil
		}
		if spelling, ok := original[*id]; ok {
			*id = spelling
			return nil
		}
		_, qualified, err := ParseReference(*id)
		if err != nil || qualified {
			return err
		}
		base.ID = *id
		*id, err = base.Encode()
		return err
	}
	var ids []*string
	for _, node := range response.Nodes {
		if node != nil {
			ids = append(ids, &node.Id)
		}
	}
	for _, hit := range response.SearchResults {
		if hit.Node != nil {
			ids = append(ids, &hit.Node.Id)
		}
	}
	for _, hit := range response.TraversalResults {
		if hit.Node != nil {
			ids = append(ids, &hit.Node.Id)
		}
	}
	for i := range response.Ids {
		ids = append(ids, &response.Ids[i])
	}
	for _, edge := range response.Edges {
		ids = append(ids, &edge.FromId, &edge.ToId)
	}
	for _, edge := range response.TraversalEdges {
		ids = append(ids, &edge.FromId, &edge.ToId)
	}
	for _, id := range ids {
		if err := qualify(id); err != nil {
			return err
		}
	}
	return nil
}

func storageResponseSlots(response *knowledgev1.ExecuteResponse) []**knowledgev1.Node {
	var nodes []**knowledgev1.Node
	for i := range response.Nodes {
		nodes = append(nodes, &response.Nodes[i])
	}
	for _, hit := range response.SearchResults {
		nodes = append(nodes, &hit.Node)
	}
	for _, hit := range response.TraversalResults {
		nodes = append(nodes, &hit.Node)
	}
	return nodes
}

func (r *Router) prepareStorageResponse(ctx context.Context, response *knowledgev1.ExecuteResponse) error {
	if err := r.hydrateStorageProxies(ctx, response); err != nil {
		return err
	}
	if ref, ok := ctx.Value(responseReferenceKey{}).(Reference); ok {
		return qualifyStorageResponse(response, ref, nil)
	}
	return nil
}
