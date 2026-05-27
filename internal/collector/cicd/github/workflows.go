// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	gogithub "github.com/google/go-github/v68/github"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cicd"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// workflowsCollector lists active workflows per repo and optionally fetches
// YAML content to extract secret references and environment deployments.
type workflowsCollector struct {
	actions actionsAPI
	repos   reposAPI
	org     string
}

func (c *workflowsCollector) Name() string { return "github-workflows" }

// Collect lists workflows for all org repos. For each active workflow it
// creates a workflow node, fetches YAML content, and emits edges for secrets,
// environments, and BELONGS_TO.
func (c *workflowsCollector) Collect(ctx context.Context) (cicd.SubCollectorResult, error) {
	var result cicd.SubCollectorResult

	repoNames, err := listRepoNames(ctx, c.repos, c.org)
	if err != nil {
		return cicd.SubCollectorResult{}, err
	}

	for _, fullName := range repoNames {
		owner, repo := splitFullName(fullName)
		if err := c.collectWorkflowsForRepo(ctx, owner, repo, &result); err != nil {
			slog.Warn("github-workflows: repo error", "repo", fullName, "error", err)
		}
	}
	slog.Debug("github-workflows: collected", "org", c.org, "workflows", len(result.Resources))
	return result, nil
}

// collectWorkflowsForRepo collects all active workflows for one repo.
func (c *workflowsCollector) collectWorkflowsForRepo(
	ctx context.Context, owner, repo string, result *cicd.SubCollectorResult,
) error {
	opts := &gogithub.ListOptions{PerPage: 100}
	for {
		wfs, resp, err := c.actions.ListWorkflows(ctx, owner, repo, opts)
		if err != nil {
			return fmt.Errorf("github-workflows: list %s/%s: %w", owner, repo, err)
		}
		for _, wf := range wfs.Workflows {
			if wf.GetState() != "active" {
				continue
			}
			c.processWorkflow(ctx, owner, repo, wf, result)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return nil
}

// processWorkflow creates a workflow node, fetches its YAML, and extracts edges.
func (c *workflowsCollector) processWorkflow(
	ctx context.Context, owner, repo string, wf *gogithub.Workflow, result *cicd.SubCollectorResult,
) {
	fullName := owner + "/" + repo
	wfID := workflowID(c.org, fullName, wf.GetPath())

	content, _ := json.Marshal(workflowMetadata{ //nolint:errchkjson // known struct
		Name:    wf.GetName(),
		Path:    wf.GetPath(),
		State:   wf.GetState(),
		HTMLURL: wf.GetHTMLURL(),
	})

	spec := cicd.ResourceSpec{
		ID:           wfID,
		Name:         wf.GetName(),
		ResourceType: "workflow",
		Provider:     "github",
		Content:      content,
		Metadata: map[string]string{
			"org":  c.org,
			"repo": fullName,
			"path": wf.GetPath(),
		},
	}
	result.Resources = append(result.Resources, spec)
	result.Edges = append(result.Edges, cicd.EdgeSpec{
		SourceID: wfID, TargetID: repoID(c.org, fullName),
		Relationship: kgtypes.EdgeBelongsTo,
	})

	// Fetch and parse YAML for secret/env edges.
	c.extractYAMLEdges(ctx, owner, repo, wf.GetPath(), wfID, result)
}

// extractYAMLEdges fetches the workflow YAML and extracts secret references
// and environment deployments.
func (c *workflowsCollector) extractYAMLEdges(
	ctx context.Context, owner, repo, path, wfID string, result *cicd.SubCollectorResult,
) {
	fc, _, _, err := c.repos.GetContents(ctx, owner, repo, path, nil)
	if err != nil || fc == nil {
		return
	}
	raw, err := fc.GetContent()
	if err != nil {
		return
	}
	fullName := owner + "/" + repo

	for _, secret := range parseSecretRefs(raw) {
		secretNodeID := secretID(c.org, fullName, "repo", secret)
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID: wfID, TargetID: secretNodeID,
			Relationship: kgtypes.EdgeUsesSecret,
		})
	}

	for _, env := range parseEnvironmentRefs(raw) {
		envNodeID := environmentID(c.org, fullName, env)
		result.Edges = append(result.Edges, cicd.EdgeSpec{
			SourceID: wfID, TargetID: envNodeID,
			Relationship: kgtypes.EdgeDeploysTo,
		})
	}
}

type workflowMetadata struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url,omitempty"`
}

// --- ID helpers ---

func workflowID(org, repoFullName, path string) string {
	return fmt.Sprintf("github:%s/Workflow/%s/%s", org, repoFullName, path)
}

func environmentID(org, repoFullName, envName string) string {
	return fmt.Sprintf("github:%s/Environment/%s/%s", org, repoFullName, envName)
}

func secretID(org, repoFullName, scope, name string) string {
	return fmt.Sprintf("github:%s/Secret/%s/%s/%s", org, repoFullName, scope, name)
}

func splitFullName(fullName string) (string, string) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return fullName, ""
	}
	return parts[0], parts[1]
}
