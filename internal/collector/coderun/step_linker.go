// SPDX-License-Identifier: Apache-2.0

package coderun

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// LinkStepsToCode creates implements edges from plan steps to code file nodes
// based on file_paths metadata. graphName is the per-repo code graph; all reads
// + writes ride the postpopulate wire helpers routed by kgtypes.GraphCode
// (→ Target.Repo==graphName). Step and file nodes already exist, so the
// KGImplements edges are emitted edges-only via one LinkEdgesBatch.
func LinkStepsToCode(ctx context.Context, gc postpopulate.GraphCaller, graphName string) error {
	steps, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCode, graphName, map[string]any{
		"type": string(kgtypes.NodeStep),
	})
	if err != nil {
		return fmt.Errorf("LinkStepsToCode: query steps: %w", err)
	}
	// With no steps the derived edge set is necessarily empty, and
	// postpopulate.LinkEdgesBatch already no-ops at len(edges)==0 — so the only
	// thing this return skips is the full file-node drain below.
	if len(steps) == 0 {
		return nil
	}

	fileNodes, err := postpopulate.BrowseAllNodes(ctx, gc, kgtypes.GraphCode, graphName, map[string]any{
		"type": string(kgtypes.NodeFile),
	})
	if err != nil {
		return fmt.Errorf("LinkStepsToCode: query files: %w", err)
	}
	fileSet := make(map[string]bool, len(fileNodes))
	for i := range fileNodes {
		fileSet[fileNodes[i].Id] = true
	}

	var edges []knowledgev1.Edge
	for i := range steps {
		n := steps[i]
		filePaths := kgtypes.Value(n, "file_paths")
		if filePaths == "" {
			continue
		}
		for fp := range strings.SplitSeq(filePaths, ",") {
			fp = strings.TrimSpace(fp)
			if fp == "" {
				continue
			}
			// Link the step to the file node only if it exists in the graph.
			if fileSet[fp] {
				edges = append(edges, knowledgev1.Edge{FromId: n.Id, ToId: fp, Type: string(kgtypes.EdgeKGImplements)})
			}
		}
	}

	if err := postpopulate.LinkEdgesBatch(ctx, gc, kgtypes.GraphCode, graphName, edges); err != nil {
		return fmt.Errorf("LinkStepsToCode: link edges: %w", err)
	}
	if len(edges) > 0 {
		slog.Info("linked plan steps to code nodes", "links", len(edges))
	}
	return nil
}
