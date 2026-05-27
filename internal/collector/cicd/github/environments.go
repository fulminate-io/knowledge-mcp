// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	gogithub "github.com/google/go-github/v68/github"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// environmentsCollector lists environments per repo with protection rules.
type environmentsCollector struct {
	repos reposAPI
	org   string
}

func (c *environmentsCollector) Name() string { return "github-environments" }

// Collect lists environments for all org repos and emits environment nodes
// with BELONGS_TO, REQUIRES_APPROVAL, and protection rule metadata.
func (c *environmentsCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	repoNames, err := listRepoNames(ctx, c.repos, c.org)
	if err != nil {
		return cicd.SubCollectorResult{}, err
	}

	for _, fullName := range repoNames {
		owner, repo := splitFullName(fullName)
		if err := c.collectEnvsForRepo(ctx, owner, repo, fullName, &result); err != nil {
			slog.Warn("github-environments: repo error", "repo", fullName, "error", err)
		}
	}
	slog.Debug("github-environments: collected", "org", c.org, "envs", len(result.Resources))
	return result, nil
}

// collectEnvsForRepo lists environments for a single repo.
func (c *environmentsCollector) collectEnvsForRepo(
	ctx context.Context, owner, repo, fullName string, result *cicd.SubCollectorResult,
) error {
	envResp, _, err := c.repos.ListEnvironments(ctx, owner, repo, nil)
	if err != nil {
		return fmt.Errorf("github-environments: list %s: %w", fullName, err)
	}
	if envResp == nil {
		return nil
	}
	for _, env := range envResp.Environments {
		buildEnvironment(c.org, fullName, env, result)
	}
	return nil
}

// buildEnvironment creates an environment node with protection rules and edges.
func buildEnvironment(org, fullName string, env *gogithub.Environment, result *cicd.SubCollectorResult) {
	envName := env.GetName()
	envNodeID := environmentID(org, fullName, envName)

	content, _ := json.Marshal(envMetadata{ //nolint:errchkjson // known struct
		Name:            envName,
		ProtectionRules: summarizeProtection(env.ProtectionRules),
	})

	spec := cicd.ResourceSpec{
		ID:           envNodeID,
		Name:         envName,
		ResourceType: "environment",
		Provider:     "github",
		Content:      content,
		Metadata: map[string]string{
			"org":  org,
			"repo": fullName,
		},
	}
	result.Resources = append(result.Resources, spec)
	result.Edges = append(result.Edges, cicd.EdgeSpec{
		SourceID: envNodeID, TargetID: repoID(org, fullName),
		Relationship: kgtypes.EdgeBelongsTo,
	})

	// Protection rules: required reviewers
	for _, rule := range env.ProtectionRules {
		if rule.GetType() != "required_reviewers" {
			continue
		}
		for _, reviewer := range rule.Reviewers {
			reviewerID := extractReviewerID(org, reviewer)
			if reviewerID != "" {
				result.Edges = append(result.Edges, cicd.EdgeSpec{
					SourceID: envNodeID, TargetID: reviewerID,
					Relationship: kgtypes.EdgeRequiresApproval,
				})
			}
		}
	}
}

// extractReviewerID returns a node ID for a reviewer, or empty if unparseable.
func extractReviewerID(org string, reviewer *gogithub.RequiredReviewer) string {
	if reviewer == nil || reviewer.Reviewer == nil {
		return ""
	}
	// The reviewer field is an interface{} — it can be a User or Team map.
	raw, err := json.Marshal(reviewer.Reviewer)
	if err != nil {
		return ""
	}
	var parsed struct {
		Login string `json:"login"`
		Slug  string `json:"slug"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	switch reviewer.GetType() {
	case "User":
		if parsed.Login != "" {
			return fmt.Sprintf("github:%s/User/%s", org, parsed.Login)
		}
	case "Team":
		if parsed.Slug != "" {
			return fmt.Sprintf("github:%s/Team/%s", org, parsed.Slug)
		}
	}
	return ""
}

type envMetadata struct {
	Name            string           `json:"name"`
	ProtectionRules []protectionInfo `json:"protection_rules,omitempty"`
}

type protectionInfo struct {
	Type      string `json:"type"`
	WaitTimer int    `json:"wait_timer,omitempty"`
	Reviewers int    `json:"reviewers,omitempty"`
}

func summarizeProtection(rules []*gogithub.ProtectionRule) []protectionInfo {
	var out []protectionInfo
	for _, r := range rules {
		info := protectionInfo{Type: r.GetType()}
		if r.WaitTimer != nil {
			info.WaitTimer = r.GetWaitTimer()
		}
		info.Reviewers = len(r.Reviewers)
		out = append(out, info)
	}
	return out
}
