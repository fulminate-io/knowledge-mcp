// SPDX-License-Identifier: Apache-2.0

package bitbucket

import (
	"context"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// bitbucketPostPopulate is the wire-shape PostPopulate hook for the bitbucket
// CI/CD family. The orchestrator (tools.runPostCollectPostPopulate) enumerates
// every cicd graph and fires this hook once per graph; graphName is the cicd
// graph name. Bitbucket graphs are named "bitbucket-<workspace>", so we recover
// the workspace from the prefix and delegate to resolveOIDCFederation. A graph
// whose name does not carry the prefix (a github graph, say) is a silent no-op.
func bitbucketPostPopulate(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	workspace := strings.TrimPrefix(graphName, "bitbucket-")
	if workspace == graphName || workspace == "" {
		// Not a bitbucket-shaped cicd graph (no prefix stripped) — no-op.
		return nil
	}
	return resolveOIDCFederation(ctx, gc, graphName, workspace)
}

func init() {
	postpopulate.Register("bitbucket", postpopulate.BreadthFamilyBroad, bitbucketPostPopulate)
}
