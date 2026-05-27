// SPDX-License-Identifier: Apache-2.0

// Package topology / dead_code_review.go — maps SSA dead functions back
// to code graph nodes by (file_path, start_line) and builds Findings.
//
// FUL-241 Phase 6: relocated client-side from pkg/topology/. The
// mapping pass reads the scoped code graph via the Execute carrier seam
// (fetchNodeIndex → engine.Compile/Execute/DecodeNodes) rather than
// holding a direct server-side graph handle, because the client stdio
// binary doesn't link the persistence layer.
package topology

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// deadCodeRow holds one mapped (or unmapped) dead function with the
// fields needed by buildDeadCodeFinding. NodeID is empty for unmapped
// rows; IsUnmapped is the explicit discriminator.
type deadCodeRow struct {
	NodeID     string
	FilePath   string // repo-relative
	Line       int
	PkgPath    string
	FuncName   string
	IsUnmapped bool
}

// reviewFlag classifies a dead function's risk profile.
type reviewFlag int

const (
	reviewFlagNone reviewFlag = iota
	reviewFlagReflect
	reviewFlagLinkname
	reviewFlagAssembly
)

func (f reviewFlag) String() string {
	switch f {
	case reviewFlagReflect:
		return "reflect"
	case reviewFlagLinkname:
		return "linkname"
	case reviewFlagAssembly:
		return "assembly"
	default:
		return "none"
	}
}

// codeNodeIndex maps "file_path:line" -> node ID for every function-ish
// node in the code graph.
type codeNodeIndex struct {
	byKey map[string]string
}

// functionishTypes lists the NodeType variants that represent a callable
// declaration.
var functionishTypes = []string{
	"function_declaration", "method_declaration",
	"function_definition", "method_definition",
	"function_item", "function", "func_literal",
}

// codeNodeKey composes the (file, line) lookup key.
func codeNodeKey(file string, line int) string {
	return fmt.Sprintf("%s:%d", file, line)
}

// mapToCodeNodes joins the deadFunc list to code graph nodes by
// (file_path, start_line) using the pre-fetched node index. Unmappable
// rows are marked with IsUnmapped=true.
func mapToCodeNodes(_ context.Context, idx *codeNodeIndex, deadFuncs []deadFunc, repoRoot string) []deadCodeRow {
	if len(deadFuncs) == 0 || idx == nil {
		return nil
	}
	rows := make([]deadCodeRow, 0, len(deadFuncs))
	for _, df := range deadFuncs {
		rows = append(rows, mapOneDeadFunc(df, idx, repoRoot))
	}
	return rows
}

// mapOneDeadFunc resolves a single deadFunc to a deadCodeRow.
func mapOneDeadFunc(df deadFunc, index *codeNodeIndex, repoRoot string) deadCodeRow {
	relPath, err := filepath.Rel(repoRoot, df.Pos.Filename)
	if err != nil || strings.HasPrefix(relPath, "..") {
		relPath = df.Pos.Filename
	}
	pkgPath := ""
	if df.Pkg != nil {
		pkgPath = df.Pkg.PkgPath
	}
	funcName := prettyDeadFuncName(df.Func)

	for _, delta := range []int{0, -1, 1} {
		key := codeNodeKey(relPath, df.Pos.Line+delta)
		if id, ok := index.byKey[key]; ok {
			return deadCodeRow{
				NodeID:   id,
				FilePath: relPath,
				Line:     df.Pos.Line,
				PkgPath:  pkgPath,
				FuncName: funcName,
			}
		}
	}
	return deadCodeRow{
		FilePath:   relPath,
		Line:       df.Pos.Line,
		PkgPath:    pkgPath,
		FuncName:   funcName,
		IsUnmapped: true,
	}
}

// prettyDeadFuncName returns the SSA function's bare Name().
func prettyDeadFuncName(fn *ssa.Function) string {
	if fn == nil {
		return ""
	}
	return fn.Name()
}

// buildDeadCodeFinding constructs one Finding from a deadCodeRow + its
// review flag. Severity stays at "notice" regardless of review flag.
func buildDeadCodeFinding(row deadCodeRow, flag reviewFlag) foundation.Finding {
	title := fmt.Sprintf("Dead function: %s.%s", row.PkgPath, row.FuncName)
	if row.IsUnmapped {
		title = fmt.Sprintf("Dead function (unindexed): %s.%s", row.PkgPath, row.FuncName)
	}
	summary := fmt.Sprintf(
		"RTA found %s.%s unreachable from any main / test entry. Defined at %s:%d.",
		row.PkgPath, row.FuncName, row.FilePath, row.Line,
	)
	if flag != reviewFlagNone {
		summary += fmt.Sprintf(" Review needed: function uses %s — RTA may be wrong.", flag)
	}
	if row.IsUnmapped {
		summary += " Code graph chunker did not index this function — drill in via the file path."
	}

	confidence := 1.0
	reviewNeeded := 0.0
	definitelyDead := 1.0
	if flag != reviewFlagNone {
		confidence = 0.5
		reviewNeeded = 1.0
		definitelyDead = 0.0
	}
	unmapped := 0.0
	if row.IsUnmapped {
		unmapped = 1.0
	}

	primary := row.NodeID
	if row.IsUnmapped {
		primary = codeNodeKey(row.FilePath, row.Line)
	}
	evidence := []string{primary}

	return foundation.Finding{
		Algorithm: "dead_code",
		Severity:  foundation.SeverityNotice,
		Title:     title,
		Summary:   summary,
		Evidence:  evidence,
		Metrics: map[string]float64{
			"line":            float64(row.Line),
			"confidence":      confidence,
			"review_needed":   reviewNeeded,
			"definitely_dead": definitelyDead,
			"unmapped":        unmapped,
		},
	}
}

// buildNodeIndexFromNodes builds a codeNodeIndex from the decoded function-ish
// *knowledgev1.Node list (the engine.DecodeNodes typed Nodes carrier). Keys each
// node by (file_path, start_line); the first ID at a key wins (dup-skip),
// matching the prior text-parse builder semantics. Nodes missing file_path /
// start_line are skipped.
func buildNodeIndexFromNodes(nodes []*knowledgev1.Node) *codeNodeIndex {
	out := &codeNodeIndex{byKey: make(map[string]string, len(nodes))}
	for i := range nodes {
		n := nodes[i]
		if n.GetFilePath() == "" || n.GetStartLine() <= 0 {
			continue
		}
		key := codeNodeKey(n.GetFilePath(), int(n.GetStartLine()))
		if _, dup := out.byKey[key]; !dup {
			out.byKey[key] = n.GetId()
		}
	}
	return out
}
