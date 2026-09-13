// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// Selector reconstructs the existing wire selector without adding storage to it.
func (r Reference) Selector() *knowledgev1.GraphSelector {
	return &knowledgev1.GraphSelector{Graph: r.Graph, Repo: r.Repo, Name: r.Name, Language: r.Language, Branch: r.Branch}
}

// resolveRequestReferences validates the entire envelope before stripping IDs.
func resolveRequestReferences(ctx context.Context, request *knowledgev1.ExecuteRequest) (context.Context, *knowledgev1.ExecuteRequest, error) {
	if request == nil || !requestHasStorageReferences(request) {
		return ctx, request, nil
	}
	req := proto.CloneOf(request)
	destination, _ := StorageDestination(ctx)
	resolver := referenceResolver{destination: destination, target: req.Target}
	for _, id := range requestReferenceIDs(req) {
		if err := resolver.resolveID(id); err != nil {
			return ctx, nil, err
		}
	}
	if err := resolver.resolveMutation(req.GetMutation()); err != nil {
		return ctx, nil, err
	}
	if resolver.selected != nil {
		ctx = WithDestination(ctx, resolver.selected.Destination)
		if req.GetQuery() != nil {
			ctx = context.WithValue(ctx, responseReferenceKey{}, *resolver.selected)
		}
		req.Target = resolver.selected.Selector()
	}
	return ctx, req, nil
}

type referenceResolver struct {
	destination Destination
	target      *knowledgev1.GraphSelector
	selected    *Reference
}

func (r *referenceResolver) resolveID(id *string) error {
	ref, qualified, err := ParseReference(*id)
	if err != nil || !qualified {
		return err
	}
	if r.selected != nil && (r.selected.Destination != ref.Destination || !proto.Equal(r.selected.Selector(), ref.Selector())) {
		return fmt.Errorf("request references different storage destinations or graphs; split target operations by destination")
	}
	r.selected = &ref
	r.destination, r.target = ref.Destination, ref.Selector()
	*id = ref.ID
	return nil
}

func (r *referenceResolver) resolveTarget(id *string) error {
	ref, qualified, err := ParseReference(*id)
	if err != nil || !qualified {
		return err
	}
	if r.destination.Storage == "cloud" && ref.Storage == "local" {
		return fmt.Errorf("cloud-to-local relationships are not allowed")
	}
	if r.destination.Storage == "cloud" && r.destination.AccountID != ref.AccountID {
		return fmt.Errorf("cross-account relationships are not allowed")
	}
	if r.destination == ref.Destination && proto.Equal(r.target, ref.Selector()) {
		*id = ref.ID
	}
	return nil
}

func requestReferenceIDs(req *knowledgev1.ExecuteRequest) []*string {
	var ids []*string
	var selection *knowledgev1.Selection
	if q := req.GetQuery(); q != nil {
		ids = append(ids, &q.ById)
		for i := range q.Ids {
			ids = append(ids, &q.Ids[i])
		}
		selection = q.Selection
	}
	if m := req.GetMutation(); m != nil {
		selection = m.Selection
		for _, node := range m.NodeBodies {
			ids = append(ids, &node.Id)
		}
		for _, edge := range m.Edges {
			ids = append(ids, &edge.FromId)
		}
		for _, item := range m.UpdateItems {
			ids = append(ids, &item.Id)
		}
	}
	if selection != nil {
		for i := range selection.Ids {
			ids = append(ids, &selection.Ids[i])
		}
		for i := range selection.FromId {
			ids = append(ids, &selection.FromId[i])
		}
	}
	return ids
}

func (r *referenceResolver) resolveMutation(m *knowledgev1.MutationPlan) error {
	if m == nil {
		return nil
	}
	metadata := []map[string]string{m.SetFields, m.SetMetadata}
	for _, node := range m.NodeBodies {
		metadata = append(metadata, node.Metadata, map[string]string{"source": node.Source})
	}
	for _, item := range m.UpdateItems {
		metadata = append(metadata, item.Metadata)
	}
	for _, values := range metadata {
		for _, value := range values {
			if err := validateArgumentReferences(value, r.destination, "mutate"); err != nil {
				return err
			}
			// The validate-only walk preserves complete metadata references.
			// Only endpoint IDs below are rewritten.
		}
	}
	for _, edge := range m.Edges {
		if err := r.resolveTarget(&edge.ToId); err != nil {
			return err
		}
	}
	if m.EdgeSpec != nil {
		return r.resolveTarget(&m.EdgeSpec.ToId)
	}
	return nil
}

// Scan only reference-bearing fields, not large node bodies. Escapes in metadata
// conservatively take the validating path because serialized JSON can escape kgref.
func requestHasStorageReferences(req *knowledgev1.ExecuteRequest) bool {
	for _, id := range requestReferenceIDs(req) {
		if strings.HasPrefix(*id, "kgref:") {
			return true
		}
	}
	m := req.GetMutation()
	if m == nil {
		return false
	}
	metadata := []map[string]string{m.SetFields, m.SetMetadata}
	for _, node := range m.NodeBodies {
		if strings.Contains(node.Source, "kgref:") || strings.Contains(node.Source, `\`) {
			return true
		}
		metadata = append(metadata, node.Metadata)
	}
	for _, item := range m.UpdateItems {
		metadata = append(metadata, item.Metadata)
	}
	for _, values := range metadata {
		for _, value := range values {
			if strings.Contains(value, "kgref:") || strings.Contains(value, `\`) {
				return true
			}
		}
	}
	for _, edge := range m.Edges {
		if strings.HasPrefix(edge.ToId, "kgref:") {
			return true
		}
	}
	return m.EdgeSpec != nil && strings.HasPrefix(m.EdgeSpec.ToId, "kgref:")
}
