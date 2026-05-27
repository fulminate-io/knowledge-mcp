// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"fmt"
	"log/slog"

	gl "gitlab.com/gitlab-org/api/client-go"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// variablesSub collects GitLab CI/CD variables (names only, never values).
type variablesSub struct {
	client *gl.Client
	group  string
	lister *projectLister
}

func (s *variablesSub) Name() string { return "gitlab-variables" }

// Collect discovers group-level and project-level CI/CD variables, emitting
// name-only resource nodes. Variable values are never accessed or stored.
func (s *variablesSub) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	// Group-level variables.
	if err := s.collectGroupVars(ctx, &result); err != nil {
		slog.Warn("gitlab-variables: group vars error", "error", err)
	}

	// Project-level variables.
	projects, err := s.lister.list(ctx)
	if err != nil {
		return cicd.SubCollectorResult{}, fmt.Errorf("gitlab-variables: %w", err)
	}
	for _, p := range projects {
		if err := s.collectProjectVars(ctx, p, &result); err != nil {
			slog.Warn("gitlab-variables: project vars error", "project", p.PathWithNamespace, "error", err)
		}
	}

	return result, nil
}

// collectGroupVars lists group-level CI/CD variables.
func (s *variablesSub) collectGroupVars(ctx context.Context, result *cicd.SubCollectorResult) error {
	opts := &gl.ListGroupVariablesOptions{
		ListOptions: gl.ListOptions{PerPage: 100},
	}
	for {
		vars, resp, err := s.client.GroupVariables.ListVariables(s.group, opts, gl.WithContext(ctx))
		if err != nil {
			return err
		}
		for _, v := range vars {
			s.addVar(v.Key, "group", s.group, result)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return nil
}

// collectProjectVars lists project-level CI/CD variables.
func (s *variablesSub) collectProjectVars(
	ctx context.Context, p *gl.Project, result *cicd.SubCollectorResult,
) error {
	opts := &gl.ListProjectVariablesOptions{
		ListOptions: gl.ListOptions{PerPage: 100},
	}
	for {
		vars, resp, err := s.client.ProjectVariables.ListVariables(p.ID, opts, gl.WithContext(ctx))
		if err != nil {
			return err
		}
		for _, v := range vars {
			varID := fmt.Sprintf("gitlab:%s/Variable/%s/%s", s.group, p.PathWithNamespace, v.Key)
			result.Resources = append(result.Resources, cicd.ResourceSpec{
				ID:           varID,
				Name:         v.Key,
				ResourceType: "variable",
				Provider:     "gitlab",
				Metadata: map[string]string{
					"scope":     "project",
					"project":   p.PathWithNamespace,
					"protected": fmt.Sprintf("%t", v.Protected),
					"masked":    fmt.Sprintf("%t", v.Masked),
				},
			})
			result.Edges = append(result.Edges, cicd.EdgeSpec{
				SourceID:     varID,
				TargetID:     fmt.Sprintf("gitlab:%s/Project/%s", s.group, p.PathWithNamespace),
				Relationship: kgtypes.EdgeBelongsTo,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return nil
}

// addVar creates a variable resource spec for a group-level variable.
func (s *variablesSub) addVar(key, scope, owner string, result *cicd.SubCollectorResult) {
	varID := fmt.Sprintf("gitlab:%s/Variable/%s/%s", s.group, owner, key)
	result.Resources = append(result.Resources, cicd.ResourceSpec{
		ID:           varID,
		Name:         key,
		ResourceType: "variable",
		Provider:     "gitlab",
		Metadata: map[string]string{
			"scope": scope,
			"group": owner,
		},
	})
	result.Edges = append(result.Edges, cicd.EdgeSpec{
		SourceID:     varID,
		TargetID:     fmt.Sprintf("gitlab:%s/Group/%s", s.group, s.group),
		Relationship: kgtypes.EdgeBelongsTo,
	})
}
