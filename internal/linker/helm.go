// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// chartInfo holds the mapping from a Helm chart name to its code graph location.
type chartInfo struct {
	repoName string
	dirPath  string
	nodeID   string // ID of the Chart.yaml node in the code graph
}

// LinkHelmCharts matches app.kubernetes.io/name and helm.sh/chart labels on
// cloud nodes against Helm Chart.yaml files in code graphs. For each match
// it emits a mutate(link, link_graph:"linkage") call with the DEPLOYS edge.
//
// Client-side port of pkg/linker.LinkHelmCharts; pure parsing helpers
// (extractChartName, stripChartVersion) ported verbatim.
func LinkHelmCharts(ctx context.Context, gc GraphCaller, opts LinkOptions) (int, error) {
	if gc == nil {
		return 0, nil
	}

	charts, err := buildChartMap(ctx, gc)
	if err != nil {
		return 0, fmt.Errorf("build chart map: %w", err)
	}
	if len(charts) == 0 {
		return 0, nil
	}

	cloudGraphs, err := listCloudGraphs(ctx, gc)
	if err != nil {
		return 0, fmt.Errorf("list cloud graphs: %w", err)
	}

	linkCount := 0
	seen := make(map[string]bool)
	for _, cg := range cloudGraphs {
		n, lerr := linkHelmInCloudGraph(ctx, gc, opts, cg, charts, seen)
		if lerr != nil {
			continue
		}
		linkCount += n
	}
	return linkCount, nil
}

// linkHelmInCloudGraph scans a single cloud graph for nodes with Helm labels
// and emits DEPLOYS edges for matching charts.
func linkHelmInCloudGraph(ctx context.Context, gc GraphCaller, opts LinkOptions, cloudGraphName string, charts map[string]chartInfo, seen map[string]bool) (int, error) {
	nodes, err := queryCloudResources(ctx, gc, cloudGraphName)
	if err != nil {
		return 0, err
	}

	linkCount := 0
	for _, node := range nodes {
		candidates := matchHelmLabels(node, charts)
		for chartName, evidence := range candidates {
			dedupKey := chartName + "\x00" + node.Id
			if seen[dedupKey] {
				continue
			}
			seen[dedupKey] = true
			ci := charts[chartName]
			if err := emitLink(ctx, gc, opts, ci.nodeID, node.Id, "DEPLOYS", "tier1-helm", evidence, 0.85); err != nil {
				continue
			}
			linkCount++
		}
	}
	return linkCount, nil
}

// matchHelmLabels checks a cloud node's labels for Helm chart matches.
func matchHelmLabels(node *knowledgev1.Node, charts map[string]chartInfo) map[string]string {
	candidates := make(map[string]string)
	if appName := kgtypes.Value(node, "label/app.kubernetes.io/name"); appName != "" {
		if _, ok := charts[appName]; ok {
			candidates[appName] = fmt.Sprintf("label app.kubernetes.io/name=%s", appName)
		}
	}
	if helmChart := kgtypes.Value(node, "label/helm.sh/chart"); helmChart != "" {
		stripped := stripChartVersion(helmChart)
		if _, ok := charts[stripped]; ok {
			candidates[stripped] = fmt.Sprintf("label helm.sh/chart=%s", helmChart)
		}
	}
	return candidates
}

// buildChartMap scans all code graphs for Chart.yaml files and builds a
// chart-name → code-graph-info index. Issues one query per code graph via
// gc.Call.
func buildChartMap(ctx context.Context, gc GraphCaller) (map[string]chartInfo, error) {
	codeGraphs, err := fetchGraphNames(ctx, gc, "code")
	if err != nil {
		return nil, err
	}
	charts := make(map[string]chartInfo)
	for _, codeName := range codeGraphs {
		if strings.Contains(codeName, "@") {
			continue
		}
		nodes, qerr := queryCodeFiles(ctx, gc, codeName)
		if qerr != nil {
			continue
		}
		for _, node := range nodes {
			base := filepath.Base(node.FilePath)
			if base != "Chart.yaml" && base != "Chart.yml" {
				continue
			}
			chartName := extractChartName(node.Content)
			if chartName == "" {
				chartName = filepath.Base(filepath.Dir(node.FilePath))
			}
			if chartName == "" || chartName == "." {
				continue
			}
			charts[chartName] = chartInfo{
				repoName: codeName,
				dirPath:  filepath.Dir(node.FilePath),
				nodeID:   node.Id,
			}
		}
	}
	return charts, nil
}

// queryCodeFiles returns EVERY NodeFile entry in a named code graph via the
// Execute carrier seam (drainNodesViaEngine → keyset pages → nodes_json
// carrier). The caller derives edges from the complete file set, so this drains
// rather than taking one bounded page.
func queryCodeFiles(ctx context.Context, gc GraphCaller, codeGraphName string) ([]*knowledgev1.Node, error) {
	nodes, err := drainNodesViaEngine(ctx, gc, map[string]any{
		"graph": "code",
		"repo":  codeGraphName,
		"type":  string(kgtypes.NodeFile),
	})
	if err != nil {
		return nil, fmt.Errorf("query code files (%s): %w", codeGraphName, err)
	}
	return nodes, nil
}

// extractChartName extracts the "name:" field from a Chart.yaml content.
// Ported verbatim from pkg/linker/helm.go.
func extractChartName(content string) string {
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(trimmed, "name:"); ok {
			val := strings.TrimSpace(after)
			val = strings.Trim(val, `"'`)
			return val
		}
	}
	return ""
}

// versionSuffixRe matches a trailing semver-like version suffix: -X.Y.Z or -X.Y
var versionSuffixRe = regexp.MustCompile(`-\d+\.\d+(\.\d+)?$`)

// stripChartVersion strips a trailing version suffix from a helm.sh/chart
// label value. Ported verbatim from pkg/linker/helm.go.
func stripChartVersion(chart string) string {
	return versionSuffixRe.ReplaceAllString(chart, "")
}
