// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// gitlabPostPopulate is the wire-shape PostPopulate hook for the gitlab CI/CD
// family. The orchestrator (tools.runPostCollectPostPopulate) enumerates every
// cicd graph and fires this hook once per graph; graphName is the cicd graph
// name. GitLab graphs are named "gitlab-<group>", so we recover the group from
// the prefix and delegate to resolveOIDCFederation. A graph whose name does not
// carry the prefix (a github/bitbucket graph, say) is a silent no-op.
func gitlabPostPopulate(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	group := strings.TrimPrefix(graphName, "gitlab-")
	if group == graphName || group == "" {
		// Not a gitlab-shaped cicd graph (no prefix stripped) — no-op.
		return nil
	}
	return resolveOIDCFederation(ctx, gc, graphName, group, gitlabOIDCIssuer())
}

func init() {
	postpopulate.Register("gitlab", gitlabPostPopulate)
}
