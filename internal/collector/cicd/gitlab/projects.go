// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"

	gl "gitlab.com/gitlab-org/api/client-go"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// projectsSub collects GitLab projects within a group (including subgroups).
type projectsSub struct {
	client *gl.Client
	group  string
	lister *projectLister
}

func (s *projectsSub) Name() string { return "gitlab-projects" }

// Collect discovers all projects in the group and emits project + group nodes.
func (s *projectsSub) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	projects, err := s.lister.list(ctx)
	if err != nil {
		return cicd.SubCollectorResult{}, fmt.Errorf("gitlab-projects: %w", err)
	}

	var result cicd.SubCollectorResult

	// Emit group node.
	result.Resources = append(result.Resources, cicd.ResourceSpec{
		ID:           fmt.Sprintf("gitlab:%s/Group/%s", s.group, s.group),
		Name:         s.group,
		ResourceType: "group",
		Provider:     "gitlab",
	})

	for _, p := range projects {
		res, edges := s.projectToSpec(p)
		result.Resources = append(result.Resources, res)
		result.Edges = append(result.Edges, edges...)
	}

	return result, nil
}

// projectToSpec converts a GitLab project API response to a ResourceSpec and edges.
func (s *projectsSub) projectToSpec(p *gl.Project) (cicd.ResourceSpec, []cicd.EdgeSpec) {
	content, _ := json.Marshal(p) //nolint:errchkjson // gitlab API struct
	projectID := fmt.Sprintf("gitlab:%s/Project/%s", s.group, p.PathWithNamespace)
	groupID := fmt.Sprintf("gitlab:%s/Group/%s", s.group, s.group)

	meta := map[string]string{
		"path_with_namespace": p.PathWithNamespace,
		"web_url":             p.WebURL,
		"default_branch":      p.DefaultBranch,
		"visibility":          string(p.Visibility),
	}
	if p.Archived {
		meta["archived"] = "true"
	}

	spec := cicd.ResourceSpec{
		ID:           projectID,
		Name:         p.Name,
		ResourceType: "project",
		Provider:     "gitlab",
		Content:      content,
		Metadata:     meta,
	}

	edges := []cicd.EdgeSpec{{
		SourceID:     projectID,
		TargetID:     groupID,
		Relationship: kgtypes.EdgeBelongsTo,
	}}

	return spec, edges
}
