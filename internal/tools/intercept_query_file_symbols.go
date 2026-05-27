// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

	"github.com/fulminate-io/knowledge-mcp/internal/engine"
)

// intercept_query_file_symbols.go is the client-side claim for the codegraph
// file_symbols surface — BOTH the standalone file_symbols tool (file_path /
// file_paths) AND query(mode:file_symbols) (path_prefix / path_prefixes mapped
// to file_path / file_paths). Ports the server HandleFileSymbols
// (cmd/knowledge-server/internal/codegraph/tools.go).
//
// BOUNDED: per file, ONE file-node ByID + ONE traverse(CONTAINS,out) for the
// symbol ids + ONE bulk ids[] hydrate (the server's collectViaContains hydrates
// per-id in a loop — the client bulk-hydrates). The suffix-match full-scan
// fallback (one Match-empty Execute) runs only when the direct CONTAINS path is
// empty (the file id is not an exact file-node match).

// fileSymbolsArgs reads BOTH arg shapes: the standalone tool's file_path/
// file_paths and the query-mode's path_prefix/path_prefixes.
type fileSymbolsArgs struct {
	Graph             string   `json:"graph"`
	Mode              string   `json:"mode"`
	FilePath          string   `json:"file_path"`
	FilePaths         []string `json:"file_paths"`
	PathPrefix        string   `json:"path_prefix"`
	PathPrefixes      []string `json:"path_prefixes"`
	Repo              string   `json:"repo"`
	Branch            string   `json:"branch"`
	IncludeSource     *bool    `json:"include_source"`
	IncludeTombstones bool     `json:"include_tombstones"`
	Format            string   `json:"format"`
}

// InterceptFileSymbols claims the standalone file_symbols tool and
// query(mode:file_symbols).
func InterceptFileSymbols(deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	var a fileSymbolsArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	switch {
	case params.Name == "file_symbols":
		// standalone tool — always ours.
	case params.Name == "query" && a.Mode == "file_symbols":
		// query-mode — map path_prefix(es) → file_path(s).
		if a.FilePath == "" {
			a.FilePath = a.PathPrefix
		}
		if len(a.FilePaths) == 0 {
			a.FilePaths = a.PathPrefixes
		}
	default:
		return false, kgtools.ToolResult{}
	}

	paths := a.FilePaths
	if a.FilePath != "" {
		paths = append([]string{a.FilePath}, paths...)
	}
	if len(paths) == 0 {
		return true, errorResult("file_symbols: file_path or file_paths required")
	}
	gc := deps.GraphClient()
	if gc == nil {
		return true, errorResult("file_symbols: graph client unavailable")
	}
	return true, composeFileSymbols(context.Background(), gc.Execute, a, paths)
}

// composeFileSymbols collects symbols per file (bounded reads) and renders.
func composeFileSymbols(ctx context.Context, exec engine.ExecuteFn, a fileSymbolsArgs, paths []string) kgtools.ToolResult {
	includeSource := a.IncludeSource == nil || *a.IncludeSource
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: a.Repo, Branch: a.Branch}

	results := make([]engine.FileSymbolsResult, 0, len(paths))
	totalNodes := 0
	for _, p := range paths {
		nodes := collectFileSymbolsClient(ctx, exec, target, p, a.IncludeTombstones)
		engine.SortFileSymbolsByStartLine(nodes)
		results = append(results, engine.FileSymbolsResult{Path: p, Nodes: nodes})
		totalNodes += len(nodes)
	}
	if totalNodes == 0 {
		return errorResult(fmt.Sprintf("no symbols found in file(s) %v", paths))
	}

	if a.Format == "json" {
		return jsonResult(engine.BuildFileSymbolsJSONPayload(paths, results, totalNodes))
	}
	var sb strings.Builder
	for i, fr := range results {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}
		sb.WriteString(engine.FormatFileSymbols(fr.Path, fr.Nodes, includeSource))
	}
	return textResult(engine.FormatCodeWithRepo(repoLabelFor(a.Repo, a.Branch), sb.String()))
}

// collectFileSymbolsClient ports collectViaContains + the suffix-match fallback:
// file ByID + traverse(CONTAINS,out) symbol ids + ONE bulk hydrate; when the
// direct CONTAINS path yields nothing, a Match-empty full-scan + suffix filter.
func collectFileSymbolsClient(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, filePath string, includeTombstones bool) []*knowledgev1.Node {
	if nodes := collectViaContainsClient(ctx, exec, target, filePath, includeTombstones); len(nodes) > 0 {
		return nodes
	}
	return collectFileSymbolsSuffixFallback(ctx, exec, target, filePath, includeTombstones)
}

// collectViaContainsClient does file ByID + CONTAINS-forward symbol-id traverse +
// ONE bulk ids[] hydrate. Returns nil when the file node is absent or not a file.
func collectViaContainsClient(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, filePath string, includeTombstones bool) []*knowledgev1.Node {
	fileResp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{ById: filePath, IncludeTombstones: includeTombstones}},
		Target: target,
	})
	if err != nil {
		return nil
	}
	fileNodes, derr := engine.DecodeNodes(fileResp)
	if derr != nil || len(fileNodes) == 0 || kgtypes.NodeType(fileNodes[0].Type) != kgtypes.NodeFile {
		return nil
	}
	fileNode := fileNodes[0]

	// CONTAINS-forward symbol ids (RETURN_MODE_IDS).
	fwd := true
	idsResp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
			Selection:         &knowledgev1.Selection{FromId: []string{filePath}, EdgeTypes: []string{string(kgtypes.EdgeContains)}},
			Forward:           &fwd,
			MaxHops:           1,
			ReturnMode:        knowledgev1.ReturnMode_RETURN_MODE_IDS,
			IncludeTombstones: includeTombstones,
		}},
		Target: target,
	})
	if err != nil {
		return []*knowledgev1.Node{fileNode}
	}
	symIDs := idsResp.GetIds()
	nodes := make([]*knowledgev1.Node, 0, len(symIDs)+1)
	nodes = append(nodes, fileNode)
	if len(symIDs) == 0 {
		return nodes
	}
	// ONE bulk ids[] hydrate (no per-symbol N+1).
	hydrateResp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Ids: symIDs, IncludeTombstones: includeTombstones}},
		Target: target,
	})
	if err != nil {
		return nodes
	}
	syms, derr := engine.DecodeNodes(hydrateResp)
	if derr != nil {
		return nodes
	}
	return append(nodes, syms...)
}

// collectFileSymbolsSuffixFallback does the Match-empty full scan + suffix filter
// (the server slow path for partial/suffix path matches).
func collectFileSymbolsSuffixFallback(ctx context.Context, exec engine.ExecuteFn, target *knowledgev1.GraphSelector, filePath string, includeTombstones bool) []*knowledgev1.Node {
	resp, err := exec(ctx, &knowledgev1.ExecuteRequest{
		Plan:   &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{Selection: &knowledgev1.Selection{}, IncludeTombstones: includeTombstones}},
		Target: target,
	})
	if err != nil {
		return nil
	}
	all, derr := engine.DecodeNodes(resp)
	if derr != nil {
		return nil
	}
	var nodes []*knowledgev1.Node
	for _, n := range all {
		if n.FilePath == filePath || strings.HasSuffix(n.FilePath, "/"+filePath) || strings.HasSuffix(n.FilePath, filePath) {
			nodes = append(nodes, n)
		}
	}
	return nodes
}
