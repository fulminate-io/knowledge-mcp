// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	gl "gitlab.com/gitlab-org/api/client-go"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// environmentsSub collects GitLab environments and their protection rules.
type environmentsSub struct {
	client *gl.Client
	group  string
	lister *projectLister
}

func (s *environmentsSub) Name() string { return "gitlab-environments" }

// Collect discovers per-project environments and their protection rules.
func (s *environmentsSub) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	projects, err := s.lister.list(ctx)
	if err != nil {
		return cicd.SubCollectorResult{}, fmt.Errorf("gitlab-environments: %w", err)
	}

	var result cicd.SubCollectorResult
	for _, p := range projects {
		if err := s.collectProject(ctx, p, &result); err != nil {
			slog.Warn("gitlab-environments: project error", "project", p.PathWithNamespace, "error", err)
		}
	}
	return result, nil
}

// collectProject lists environments for a single project.
func (s *environmentsSub) collectProject(
	ctx context.Context, p *gl.Project, result *cicd.SubCollectorResult,
) error {
	opts := &gl.ListEnvironmentsOptions{
		ListOptions: gl.ListOptions{PerPage: 100},
	}

	projectID := fmt.Sprintf("gitlab:%s/Project/%s", s.group, p.PathWithNamespace)

	for {
		envs, resp, err := s.client.Environments.ListEnvironments(p.ID, opts, gl.WithContext(ctx))
		if err != nil {
			return err
		}
		for _, env := range envs {
			s.addEnvironment(p, env, projectID, result)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Also check protected environments for approval rules.
	s.collectProtectedEnvs(ctx, p, result)
	return nil
}

// addEnvironment emits an environment resource and its edges.
func (s *environmentsSub) addEnvironment(
	p *gl.Project, env *gl.Environment, projectID string, result *cicd.SubCollectorResult,
) {
	envID := fmt.Sprintf("gitlab:%s/Environment/%s/%s", s.group, p.PathWithNamespace, env.Name)

	meta := map[string]string{
		"project": p.PathWithNamespace,
		"state":   env.State,
	}
	if env.Tier != "" {
		meta["tier"] = env.Tier
	}
	if env.ExternalURL != "" {
		meta["external_url"] = env.ExternalURL
	}

	result.Resources = append(result.Resources, cicd.ResourceSpec{
		ID:           envID,
		Name:         env.Name,
		ResourceType: "environment",
		Provider:     "gitlab",
		Metadata:     meta,
	})

	result.Edges = append(result.Edges, cicd.EdgeSpec{
		SourceID: envID, TargetID: projectID, Relationship: kgtypes.EdgeBelongsTo,
	})
}

// collectProtectedEnvs checks for protected environments with approval rules.
func (s *environmentsSub) collectProtectedEnvs(
	ctx context.Context, p *gl.Project, result *cicd.SubCollectorResult,
) {
	opts := &gl.ListProtectedEnvironmentsOptions{
		ListOptions: gl.ListOptions{PerPage: 100},
	}

	protectedEnvs, _, err := s.client.ProtectedEnvironments.ListProtectedEnvironments(
		p.ID, opts, gl.WithContext(ctx),
	)
	if err != nil {
		slog.Debug("gitlab-environments: protected envs error", "project", p.PathWithNamespace, "error", err)
		return
	}

	for _, pe := range protectedEnvs {
		if pe.RequiredApprovalCount <= 0 {
			continue
		}
		envID := fmt.Sprintf("gitlab:%s/Environment/%s/%s", s.group, p.PathWithNamespace, pe.Name)
		ruleID := fmt.Sprintf("gitlab:%s/ProtectionRule/%s/%s", s.group, p.PathWithNamespace, pe.Name)

		result.Resources = append(result.Resources, cicd.ResourceSpec{
			ID:           ruleID,
			Name:         fmt.Sprintf("%s protection", pe.Name),
			ResourceType: "protection-rule",
			Provider:     "gitlab",
			Metadata: map[string]string{
				"environment":             pe.Name,
				"project":                 p.PathWithNamespace,
				"required_approval_count": strconv.FormatInt(pe.RequiredApprovalCount, 10),
			},
		})
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID: envID, TargetID: ruleID, Relationship: kgtypes.EdgeRequiresApproval,
		})
	}
}
