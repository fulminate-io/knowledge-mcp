// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

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
// per-id in a loop — the client bulk-hydrates). The suffix-match fallback runs
// only when the direct CONTAINS path is empty (the file id is not an exact
// file-node match), and is itself bounded in two stages: a keyset-paged
// RETURN_MODE_IDS index of file-node ids, memoized once per tool call, resolves
// the suffix client-side; each resolved path then takes ONE index-backed
// file_path-EQ browse. Neither stage reads the whole graph.

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
func InterceptFileSymbols(ctx context.Context, deps ClientDeps, params kgtools.CallToolParams) (bool, kgtools.ToolResult) {
	var a fileSymbolsArgs
	if err := json.Unmarshal(params.Arguments, &a); err != nil {
		return false, kgtools.ToolResult{}
	}
	switch {
	case params.Name == "file_symbols":
		// standalone tool — always ours.
		//
		// The standalone arm accounts against FILE_SYMBOLS' OWN schema, and the
		// query arm below is left to the query gate. Both halves matter: query
		// declares ~50 params file_symbols does not, so accounting a standalone
		// call against query's schema would ACCEPT those and drop them silently,
		// and would name query in a refusal the file_symbols caller has to act on.
		if err := rejectUndeclaredParams("file_symbols", "", FileSymbolsToolDef().InputSchema.Properties, params.Arguments); err != nil {
			return true, errorResult(err.Error())
		}
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

	if err := accountQueryParams(armFileSymbols, params.Arguments); err != nil {
		return true, errorResult(err.Error())
	}
	paths := a.FilePaths
	if a.FilePath != "" {
		paths = append([]string{a.FilePath}, paths...)
	}
	if len(paths) == 0 {
		return true, errorResult("file_symbols: file_path or file_paths required")
	}
	gc := deps.GraphCaller()
	if gc == nil {
		return true, errorResult("file_symbols: graph client unavailable")
	}
	return true, composeFileSymbols(ctx, gc.Execute, a, paths)
}

// composeFileSymbols collects symbols per file (bounded reads) and renders.
func composeFileSymbols(ctx context.Context, exec engine.ExecuteFn, a fileSymbolsArgs, paths []string) kgtools.ToolResult {
	includeSource := a.IncludeSource == nil || *a.IncludeSource
	target := &knowledgev1.GraphSelector{Graph: "code", Repo: a.Repo, Branch: a.Branch}

	results := make([]engine.FileSymbolsResult, 0, len(paths))
	totalNodes := 0
	// ONE collector for the whole tool call: the file index its suffix fallback
	// needs is then browsed at most once no matter how many paths file_paths
	// carries. The per-path loop was the multiplier.
	c := newFileSymbolsCollector(exec, target, a.IncludeTombstones)
	for _, p := range paths {
		nodes := c.collect(ctx, p)
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

// fileSymbolsSuffixFileCap bounds how many files one suffix may resolve to. A
// degenerate suffix such as ".go" matches every file in the repo; the previous
// full-scan fallback answered that with the entire graph's symbols, so a
// deterministic sorted cap is both strictly more useful and bounded. This is a
// deliberate behavior departure for degenerate inputs only — any suffix that
// names a real file resolves to one or two paths, far under the cap.
const fileSymbolsSuffixFileCap = 64

// fileSymbolsPerFileCap bounds the per-file symbol browse. Set (rather than left
// at 0 = no cap) so a predicate-blind server that ignored the file_path predicate
// cannot stream the whole graph back.
const fileSymbolsPerFileCap = 5000

// fileSymbolsCollector is the per-tool-call collector for file symbols. It owns
// the memoized file-node id index, which is what makes the suffix fallback cheap:
// the index is drained AT MOST ONCE per tool call and reused across every
// requested path, where the previous free function paid an unbounded whole-graph
// read per path that missed.
type fileSymbolsCollector struct {
	exec              engine.ExecuteFn
	target            *knowledgev1.GraphSelector
	includeTombstones bool

	once  sync.Once
	index []string
}

func newFileSymbolsCollector(exec engine.ExecuteFn, target *knowledgev1.GraphSelector, includeTombstones bool) *fileSymbolsCollector {
	return &fileSymbolsCollector{exec: exec, target: target, includeTombstones: includeTombstones}
}

// collect ports collectViaContains + the suffix-match fallback: file ByID +
// traverse(CONTAINS,out) symbol ids + ONE bulk hydrate; when the direct CONTAINS
// path yields nothing, the two-stage bounded suffix fallback.
func (c *fileSymbolsCollector) collect(ctx context.Context, filePath string) []*knowledgev1.Node {
	if nodes := collectViaContainsClient(ctx, c.exec, c.target, filePath, c.includeTombstones); len(nodes) > 0 {
		return nodes
	}
	return c.suffixFallback(ctx, filePath)
}

// fileIndex returns every file-node id in the graph, drained in bounded id-keyset
// pages and memoized for the life of the collector. The ids ARE the file paths:
// the collector builds each file node with Id == FilePath (parser/populate.go).
//
// sync.Once makes the once-per-tool-call property structural rather than a
// consequence of the callers happening to be serial.
func (c *fileSymbolsCollector) fileIndex(ctx context.Context) []string {
	c.once.Do(func() {
		ids, err := engine.DrainKeysetIDs(func(afterID string) ([]string, error) {
			cursor := afterID
			resp, rerr := c.exec(ctx, &knowledgev1.ExecuteRequest{
				Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
					Selection:  &knowledgev1.Selection{NodeType: string(kgtypes.NodeFile)},
					ReturnMode: knowledgev1.ReturnMode_RETURN_MODE_IDS,
					Limit:      int32(engine.BrowsePageSize),
					// AfterId is ALWAYS SET, including the empty cursor on page 1:
					// its PRESENCE is what selects the keyset browse. Omitting it on
					// page 1 makes the backend page in its own default order, and the
					// cursor taken from that page then skips every lower id. Offset is
					// never set — the two cursors are mutually exclusive server-side.
					AfterId:           &cursor,
					SkipTotal:         true, // the drain reads only ids, never Total
					IncludeTombstones: c.includeTombstones,
				}},
				Target: c.target,
			})
			if rerr != nil {
				return nil, rerr
			}
			return resp.GetIds(), nil
		}, engine.BrowsePageSize)
		if err != nil {
			return
		}
		c.index = ids
	})
	return c.index
}

// suffixFallback resolves filePath as a suffix against the memoized file index,
// then reads each resolved file's symbols with ONE index-backed file_path-EQ
// browse. It replaces a per-path unbounded whole-graph full-hydration read.
//
// A file_path-EQ browse — not a second pass through the CONTAINS path — is what
// reproduces the old fallback's set exactly: anonymous declarations carry a
// file_path but have no inbound CONTAINS edge, so re-entering CONTAINS with a
// resolved exact id would silently drop them.
func (c *fileSymbolsCollector) suffixFallback(ctx context.Context, filePath string) []*knowledgev1.Node {
	// An empty path used to match every node in the graph (HasSuffix(x, "") is
	// always true), so the fallback returned the WHOLE graph. That pathological
	// case is removed deliberately.
	if filePath == "" {
		return nil
	}
	// Re-stamp over the tool-level file_symbols term: the fallback is a completely
	// different load shape from the bounded CONTAINS path, and telling the two
	// apart in the metrics is exactly the attribution problem this dimension
	// exists to solve. Both the index drain and the per-path browses carry it.
	ctx = graphclient.WithOperation(ctx, graphclient.OpFileSymbolsSuffixFallback)

	var matches []string
	for _, f := range c.fileIndex(ctx) {
		if f == filePath || strings.HasSuffix(f, "/"+filePath) || strings.HasSuffix(f, filePath) {
			matches = append(matches, f)
		}
	}
	sort.Strings(matches)
	if len(matches) > fileSymbolsSuffixFileCap {
		matches = matches[:fileSymbolsSuffixFileCap]
	}

	var nodes []*knowledgev1.Node
	for _, f := range matches {
		resp, err := c.exec(ctx, &knowledgev1.ExecuteRequest{
			Plan: &knowledgev1.ExecuteRequest_Query{Query: &knowledgev1.QueryPlan{
				Selection: &knowledgev1.Selection{FieldPredicates: []*knowledgev1.FieldPredicate{
					{Field: "file_path", Op: knowledgev1.MetadataPredicate_OP_EQ, Value: f},
				}},
				Limit:             fileSymbolsPerFileCap,
				SkipTotal:         true,
				IncludeTombstones: c.includeTombstones,
			}},
			Target: c.target,
		})
		if err != nil {
			continue
		}
		page, derr := engine.DecodeNodes(resp)
		if derr != nil {
			continue
		}
		// Client-side FilePath guard (DEFENSE-IN-DEPTH, store-portable — do NOT
		// delete): against a server that HONORS field_predicates (the OSS embedded
		// executor via nodeMatchesField, or the cloud executor's fieldPredicateClauses)
		// this is a cheap no-op over the handful of rows the WHERE already narrowed;
		// against a predicate-BLIND server it is the load-bearing guard that stops
		// wrong-file symbols being returned.
		for _, n := range page {
			if n.FilePath == f {
				nodes = append(nodes, n)
			}
		}
	}
	return nodes
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
