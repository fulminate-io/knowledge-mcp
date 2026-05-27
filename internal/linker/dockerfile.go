// SPDX-License-Identifier: Apache-2.0

package linker

import (
	"bufio"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// LinkDockerfiles parses COPY and ADD directives in Dockerfile nodes within
// code graphs. For each directive referencing a local source file or
// directory in the same repo, it emits a mutate(link, link_graph:"linkage")
// call with the BUILDS edge.
//
// Client-side port of pkg/linker.LinkDockerfiles; parseCopyDirectives and
// isDockerfile are pure helpers ported verbatim.
func LinkDockerfiles(ctx context.Context, gc GraphCaller, opts LinkOptions) (int, error) {
	if gc == nil {
		return 0, nil
	}
	codeGraphs, err := fetchGraphNames(ctx, gc, "code")
	if err != nil {
		return 0, fmt.Errorf("list code graphs: %w", err)
	}
	linkCount := 0
	for _, name := range codeGraphs {
		if strings.Contains(name, "@") {
			continue
		}
		n, lerr := linkDockerfilesInRepo(ctx, gc, opts, name)
		if lerr != nil {
			continue
		}
		linkCount += n
	}
	return linkCount, nil
}

// linkDockerfilesInRepo processes all Dockerfiles in a single code repo and
// emits BUILDS edges for each resolvable COPY/ADD source.
func linkDockerfilesInRepo(ctx context.Context, gc GraphCaller, opts LinkOptions, repoName string) (int, error) {
	files, err := queryCodeFiles(ctx, gc, repoName)
	if err != nil {
		return 0, err
	}
	pkgs, err := queryCodePackages(ctx, gc, repoName)
	if err != nil {
		return 0, err
	}

	filePaths := make(map[string]string, len(files))
	for _, n := range files {
		if n.FilePath != "" {
			filePaths[n.FilePath] = n.Id
		}
	}
	dirNodes := make(map[string]string, len(pkgs))
	for _, n := range pkgs {
		if n.Id != "" {
			dirNodes[n.Id] = n.Id
		}
	}

	linkCount := 0
	for _, node := range files {
		if !isDockerfile(node.FilePath) {
			continue
		}
		n := linkSingleDockerfile(ctx, gc, opts, node, filePaths, dirNodes)
		linkCount += n
	}
	return linkCount, nil
}

// linkSingleDockerfile parses one Dockerfile and emits one BUILDS edge per
// resolvable COPY/ADD source.
func linkSingleDockerfile(ctx context.Context, gc GraphCaller, opts LinkOptions, node *knowledgev1.Node, filePaths, dirNodes map[string]string) int {
	content := node.Content
	if content == "" {
		return 0
	}
	dockerfileDir := filepath.Dir(node.FilePath)
	directives := parseCopyDirectives(content)

	linkCount := 0
	for _, srcPath := range directives {
		resolved := srcPath
		if !filepath.IsAbs(srcPath) {
			resolved = filepath.Join(dockerfileDir, srcPath)
		}
		resolved = filepath.Clean(resolved)

		targetNodeID := resolveDockerfileSrc(resolved, filePaths, dirNodes)
		if targetNodeID == "" {
			continue
		}
		evidence := fmt.Sprintf("COPY/ADD %s in %s", srcPath, node.FilePath)
		if err := emitLink(ctx, gc, opts, node.Id, targetNodeID, "BUILDS", "tier1-dockerfile", evidence, 0.95); err != nil {
			continue
		}
		linkCount++
	}
	return linkCount
}

// resolveDockerfileSrc maps a path resolved relative to the Dockerfile dir
// against the known set of file paths and package directories in the repo.
// Ported verbatim from pkg/linker/dockerfile.go.
func resolveDockerfileSrc(resolved string, filePaths, dirNodes map[string]string) string {
	if nid, ok := filePaths[resolved]; ok {
		return nid
	}
	if nid, ok := dirNodes[resolved]; ok {
		return nid
	}
	for dir, nid := range dirNodes {
		if dir == resolved || strings.HasPrefix(dir, resolved+"/") {
			return nid
		}
	}
	return ""
}

// isDockerfile returns true if the file path looks like a Dockerfile.
// Ported verbatim from pkg/linker/dockerfile.go.
func isDockerfile(path string) bool {
	base := filepath.Base(path)
	lower := strings.ToLower(base)
	return lower == "dockerfile" ||
		strings.HasPrefix(lower, "dockerfile.") ||
		strings.HasSuffix(lower, ".dockerfile")
}

// parseCopyDirectives extracts source paths from COPY and ADD directives in
// Dockerfile content. Ported verbatim from pkg/linker/dockerfile.go.
func parseCopyDirectives(content string) []string {
	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(content))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "COPY ") && !strings.HasPrefix(upper, "ADD ") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}

		parts = parts[1:]

		hasFrom := false
		i := 0
		for i < len(parts) && strings.HasPrefix(parts[i], "--") {
			if strings.HasPrefix(parts[i], "--from") {
				hasFrom = true
			}
			i++
		}
		parts = parts[i:]

		if hasFrom {
			continue
		}

		if len(parts) < 2 {
			continue
		}

		srcs := parts[:len(parts)-1]
		for _, src := range srcs {
			if strings.HasPrefix(strings.ToLower(src), "http://") ||
				strings.HasPrefix(strings.ToLower(src), "https://") {
				continue
			}
			if src == "." || src == "./" || src == "*" {
				continue
			}
			paths = append(paths, src)
		}
	}

	return paths
}

// queryCodePackages returns NodePackage entries from a named code graph via
// the Execute carrier seam (browseNodesViaEngine → nodes_json carrier).
func queryCodePackages(ctx context.Context, gc GraphCaller, codeGraphName string) ([]*knowledgev1.Node, error) {
	nodes, err := browseNodesViaEngine(ctx, gc, map[string]any{
		"graph": "code",
		"repo":  codeGraphName,
		"type":  string(kgtypes.NodePackage),
		"limit": 0,
	})
	if err != nil {
		return nil, fmt.Errorf("query code packages (%s): %w", codeGraphName, err)
	}
	return nodes, nil
}
