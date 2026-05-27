// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"
	"sort"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// render_code_symbols.go ports the file_symbols + list_modules renderers from the
// server codegraph/tools.go. The composers (cmd/knowledge/internal/tools) drive
// the generic Execute reads (file ByID + CONTAINS traverse + bulk hydrate for
// file_symbols; Match(package)+Match(file) for list_modules) and render via these.

// FileSymbolsResult holds the collected nodes for one file path (port of
// fileSymbolsResult).
type FileSymbolsResult struct {
	Path  string
	Nodes []*knowledgev1.Node
}

// fileSymbolsJSONNode / FileSymbolsJSONResponse port the server JSON shapes — the
// AST tool's bulk enclosing-node lookup consumes this exact shape.
type fileSymbolsJSONNode struct {
	ID         string `json:"id"`
	SymbolName string `json:"symbol_name,omitempty"`
	Type       string `json:"type"`
	FilePath   string `json:"file_path"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Signature  string `json:"signature,omitempty"`
}

// FileSymbolsJSONResponse is the format=json file_symbols payload.
type FileSymbolsJSONResponse struct {
	FilePaths []string              `json:"file_paths"`
	Total     int                   `json:"total"`
	Symbols   []fileSymbolsJSONNode `json:"symbols"`
}

// BuildFileSymbolsJSONPayload ports the server buildFileSymbolsJSONPayload.
func BuildFileSymbolsJSONPayload(paths []string, results []FileSymbolsResult, totalNodes int) FileSymbolsJSONResponse {
	resp := FileSymbolsJSONResponse{
		FilePaths: paths,
		Total:     totalNodes,
		Symbols:   make([]fileSymbolsJSONNode, 0, totalNodes),
	}
	for _, fr := range results {
		for _, n := range fr.Nodes {
			resp.Symbols = append(resp.Symbols, fileSymbolsJSONNode{
				ID:         n.Id,
				SymbolName: n.SymbolName,
				Type:       n.Type,
				FilePath:   n.FilePath,
				StartLine:  int(n.StartLine),
				EndLine:    int(n.EndLine),
				Signature:  n.Signature,
			})
		}
	}
	return resp
}

// FormatFileSymbols ports the server formatFileSymbols markdown — "# Symbols in
// <file> (N found)" + optional file summary + per-symbol "### n. <name>" blocks
// (skipping file/module/repository container nodes).
func FormatFileSymbols(filePath string, nodes []*knowledgev1.Node, includeSource bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Symbols in %s (%d found)\n\n", filePath, len(nodes))
	for _, n := range nodes {
		if n.Type == "file" && n.Summary != "" {
			fmt.Fprintf(&sb, "**File summary:** %s\n\n", n.Summary)
			break
		}
	}
	for i, n := range nodes {
		if n.Type == "file" || n.Type == "module" || n.Type == "repository" {
			continue
		}
		fmt.Fprintf(&sb, "### %d. %s (%s) — L%d-%d\n", i+1, CodeDisplayName(n), n.Type, n.StartLine, n.EndLine)
		if n.SymbolName != "" && n.Summary != "" {
			fmt.Fprintf(&sb, "Summary: %s\n", n.Summary)
		}
		if n.Signature != "" {
			fmt.Fprintf(&sb, "Signature: `%s`\n", n.Signature)
		}
		if includeSource && n.Content != "" {
			fmt.Fprintf(&sb, "\n```%s\n%s\n```\n\n", n.Language, n.Content)
		} else {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// SortFileSymbolsByStartLine sorts the collected nodes ascending by StartLine
// (the order HandleFileSymbols applies before rendering).
func SortFileSymbolsByStartLine(nodes []*knowledgev1.Node) {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].StartLine < nodes[j].StartLine })
}

// RenderListModules ports the server ListModulesOnGraph rollup + render. The
// composer supplies the package nodes + file nodes; this builds the
// longest-prefix file-count rollup and renders the "### Modules (n)" listing.
// Returns "" when there are no modules (caller renders "No modules found.").
func RenderListModules(packages, files []*knowledgev1.Node, pathPrefix string) string {
	type moduleInfo struct {
		node      *knowledgev1.Node
		fileCount int
	}
	modules := map[string]*moduleInfo{}
	for _, n := range packages {
		if pathPrefix != "" && !strings.HasPrefix(n.Id, pathPrefix) {
			continue
		}
		modules[n.Id] = &moduleInfo{node: n}
	}
	for _, n := range files {
		if n.FilePath == "" {
			continue
		}
		bestModule := ""
		for modID := range modules {
			if strings.HasPrefix(n.FilePath, modID) && len(modID) > len(bestModule) {
				bestModule = modID
			}
		}
		if bestModule != "" {
			modules[bestModule].fileCount++
		}
	}
	if len(modules) == 0 {
		return ""
	}
	modIDs := make([]string, 0, len(modules))
	for id := range modules {
		modIDs = append(modIDs, id)
	}
	sort.Strings(modIDs)
	var sb strings.Builder
	fmt.Fprintf(&sb, "### Modules (%d)\n\n", len(modIDs))
	for _, id := range modIDs {
		m := modules[id]
		fmt.Fprintf(&sb, "**%s** (%d files)\n", id, m.fileCount)
		if m.node.Summary != "" {
			fmt.Fprintf(&sb, "%s\n", m.node.Summary)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
