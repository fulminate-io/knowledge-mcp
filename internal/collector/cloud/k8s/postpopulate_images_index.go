// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/cloud"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// registryIndex holds cloud-registry nodes indexed for fast image matching.
type registryIndex struct {
	ecrRepos map[string]string // "account:region:repoName" → node ID (ARN)
	acrHosts map[string]string // "myregistry.azurecr.io" → node ID
	arRepos  map[string]string // "project/repo" → node ID
}

func (ri *registryIndex) empty() bool {
	return len(ri.ecrRepos) == 0 && len(ri.acrHosts) == 0 && len(ri.arRepos) == 0
}

// match returns the registry node ID for the given image ref, or "" if none.
func (ri *registryIndex) match(ref cloud.ImageRef) string {
	switch ref.RegistryKind() {
	case cloud.RegistryECR:
		return ri.matchECR(ref)
	case cloud.RegistryACR:
		return ri.matchACR(ref)
	case cloud.RegistryArtifactRegistry:
		return ri.matchAR(ref)
	case cloud.RegistryGCR:
		return ri.matchGCR(ref)
	default:
		return ""
	}
}

func (ri *registryIndex) matchECR(ref cloud.ImageRef) string {
	// ECR hostname: <account>.dkr.ecr.<region>.amazonaws.com
	parts := strings.SplitN(ref.Registry, ".", 6)
	if len(parts) < 6 {
		return ""
	}
	account, region := parts[0], parts[3]
	repo := ref.Repository
	key := account + ":" + region + ":" + repo
	return ri.ecrRepos[key]
}

func (ri *registryIndex) matchACR(ref cloud.ImageRef) string {
	return ri.acrHosts[ref.Registry]
}

func (ri *registryIndex) matchAR(ref cloud.ImageRef) string {
	// AR hostname: <region>-docker.pkg.dev, repo path: project/repo/image
	// AR nodes have IDs like "projects/<project>/locations/<loc>/repositories/<repo>"
	// We need to match project+repo from the image path.
	repoParts := strings.SplitN(ref.Repository, "/", 3)
	if len(repoParts) < 2 {
		return ""
	}
	key := repoParts[0] + "/" + repoParts[1] // project/repo
	return ri.arRepos[key]
}

func (ri *registryIndex) matchGCR(ref cloud.ImageRef) string {
	// GCR -> AR migration: try matching project/repo against AR index.
	// GCR images: gcr.io/<project>/<image> or <region>.gcr.io/<project>/<image>
	repoParts := strings.SplitN(ref.Repository, "/", 2)
	if len(repoParts) < 2 {
		return ""
	}
	key := repoParts[0] + "/" + repoParts[1]
	return ri.arRepos[key]
}

// buildRegistryIndex queries ECR, ACR, and Artifact Registry nodes and
// indexes them for matching.
func buildRegistryIndex(ctx context.Context, gc postpopulate.GraphCaller, graphName string) (*registryIndex, error) {
	ri := &registryIndex{
		ecrRepos: make(map[string]string),
		acrHosts: make(map[string]string),
		arRepos:  make(map[string]string),
	}

	if err := indexECRRepos(ctx, gc, graphName, ri); err != nil {
		return nil, err
	}
	if err := indexACRRegistries(ctx, gc, graphName, ri); err != nil {
		return nil, err
	}
	if err := indexARRepos(ctx, gc, graphName, ri); err != nil {
		return nil, err
	}
	return ri, nil
}

// indexECRRepos queries ecr-repository nodes and indexes by account:region:repoName.
func indexECRRepos(ctx context.Context, gc postpopulate.GraphCaller, graphName string, ri *registryIndex) error {
	nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("ecr-repository"))
	if err != nil {
		return err
	}
	for _, n := range nodes {
		// ECR ARN: arn:aws:ecr:<region>:<account>:repository/<name>
		account, region, repo := parseECRArn(n.Id)
		if account != "" {
			ri.ecrRepos[account+":"+region+":"+repo] = n.Id
		}
	}
	return nil
}

// parseECRArn extracts account, region, and repo from an ECR ARN.
func parseECRArn(arn string) (account, region, repo string) {
	// arn:aws:ecr:<region>:<account>:repository/<repo>
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[2] != "ecr" {
		return "", "", ""
	}
	region = parts[3]
	account = parts[4]
	repoPath := parts[5]
	const prefix = "repository/"
	if strings.HasPrefix(repoPath, prefix) {
		repo = repoPath[len(prefix):]
	}
	return account, region, repo
}

// indexACRRegistries queries ACR nodes and indexes by loginServer hostname.
func indexACRRegistries(ctx context.Context, gc postpopulate.GraphCaller, graphName string, ri *registryIndex) error {
	nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("Microsoft.ContainerRegistry/registries"))
	if err != nil {
		return err
	}
	for _, n := range nodes {
		ls := kgtypes.Value(n, "loginServer")
		if ls != "" {
			ri.acrHosts[ls] = n.Id
		}
	}
	return nil
}

// indexARRepos queries Artifact Registry nodes and indexes by project/repo.
func indexARRepos(ctx context.Context, gc postpopulate.GraphCaller, graphName string, ri *registryIndex) error {
	nodes, err := postpopulate.BrowseNodes(ctx, gc, kgtypes.GraphCloud, graphName, k8sResourceQuery("gcp:artifactregistry:repository"))
	if err != nil {
		return err
	}
	for _, n := range nodes {
		// AR ID: projects/<project>/locations/<loc>/repositories/<repo>
		project, repo := parseARID(n.Id)
		if project != "" {
			ri.arRepos[project+"/"+repo] = n.Id
		}
	}
	return nil
}

// parseARID extracts project and repo from an Artifact Registry resource name.
func parseARID(id string) (project, repo string) {
	// projects/<project>/locations/<loc>/repositories/<repo>
	parts := strings.Split(id, "/")
	var p, r string
	for i, seg := range parts {
		if seg == "projects" && i+1 < len(parts) {
			p = parts[i+1]
		}
		if seg == "repositories" && i+1 < len(parts) {
			r = parts[i+1]
		}
	}
	return p, r
}
