// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	gl "gitlab.com/gitlab-org/api/client-go"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// deploymentsSub collects GitLab deployments for each project.
type deploymentsSub struct {
	client *gl.Client
	group  string
	lister *projectLister
}

func (s *deploymentsSub) Name() string { return "gitlab-deployments" }

// Collect discovers per-project deployments and emits EdgeDeploysTo edges.
func (s *deploymentsSub) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	projects, err := s.lister.list(ctx)
	if err != nil {
		return cicd.SubCollectorResult{}, fmt.Errorf("gitlab-deployments: %w", err)
	}

	var result cicd.SubCollectorResult
	for _, p := range projects {
		if err := s.collectProject(ctx, p, &result); err != nil {
			slog.Warn("gitlab-deployments: project error", "project", p.PathWithNamespace, "error", err)
		}
	}
	return result, nil
}

// collectProject lists recent deployments for a single project.
func (s *deploymentsSub) collectProject(
	ctx context.Context, p *gl.Project, result *cicd.SubCollectorResult,
) error {
	opts := &gl.ListProjectDeploymentsOptions{
		ListOptions: gl.ListOptions{PerPage: 20},
		OrderBy:     new("created_at"),
		Sort:        new("desc"),
	}

	projectID := fmt.Sprintf("gitlab:%s/Project/%s", s.group, p.PathWithNamespace)

	deployments, _, err := s.client.Deployments.ListProjectDeployments(p.ID, opts, gl.WithContext(ctx))
	if err != nil {
		return err
	}

	for _, d := range deployments {
		s.addDeployment(p, d, projectID, result)
	}
	return nil
}

// addDeployment emits a deployment resource with edges to the project and
// target environment.
func (s *deploymentsSub) addDeployment(
	p *gl.Project, d *gl.Deployment, projectID string, result *cicd.SubCollectorResult,
) {
	depID := fmt.Sprintf("gitlab:%s/Deployment/%s/%d", s.group, p.PathWithNamespace, d.ID)
	content, _ := json.Marshal(d) //nolint:errchkjson // gitlab API struct

	meta := map[string]string{
		"project": p.PathWithNamespace,
		"status":  d.Status,
		"ref":     d.Ref,
		"sha":     d.SHA,
	}
	if d.Environment != nil {
		meta["environment"] = d.Environment.Name
	}

	result.Resources = append(result.Resources, cicd.ResourceSpec{
		ID:           depID,
		Name:         fmt.Sprintf("deployment #%d", d.ID),
		ResourceType: "deployment",
		Provider:     "gitlab",
		Content:      content,
		Metadata:     meta,
	})

	result.Edges = append(result.Edges, cicd.EdgeSpec{
		SourceID: depID, TargetID: projectID, Relationship: kgtypes.EdgeBelongsTo,
	})

	// EdgeDeploysTo: deployment → environment.
	if d.Environment != nil {
		envID := fmt.Sprintf("gitlab:%s/Environment/%s/%s",
			s.group, p.PathWithNamespace, d.Environment.Name)
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID: depID, TargetID: envID, Relationship: kgtypes.EdgeDeploysTo,
		})
	}
}
