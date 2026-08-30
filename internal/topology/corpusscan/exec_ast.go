// SPDX-License-Identifier: Apache-2.0

// exec_ast.go executes ast_pattern checks against the target repo's working
// tree, delegating wholesale to the ast engine's exported surface.

package corpusscan

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/ast"
	"github.com/fulminate-io/knowledge-mcp/internal/corpus"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// executeAstCheck runs one ast_pattern check over req.RepoRoot and returns one
// finding per match site.
//
// Checks run SERIALLY by design. ast.Match already fans out across the machine's
// cores inside a single walk, so N concurrent walks contend for the same worker
// pool rather than adding throughput.
func executeAstCheck(ctx context.Context, req foundation.Request, entry corpusEntry) ([]foundation.Finding, error) {
	c := entry.Check
	sev, err := checkSeverity(c)
	if err != nil {
		return nil, err
	}
	lang := c.Language
	// A denied grammar can never execute a pattern, so a walk there is a
	// guaranteed empty result masquerading as a clean one.
	if ast.IsDeniedLanguage(lang) {
		return nil, fmt.Errorf("topology/%s: check %q is written against %s=%q, which the ast engine denies — a walk there could only ever report clean",
			AnalyzerName, c.ID, corpus.MetaLanguage, lang)
	}
	pat, err := ast.Parse(c.Pattern)
	if err != nil {
		return nil, fmt.Errorf("topology/%s: check %q: %s does not parse: %w", AnalyzerName, c.ID, corpus.MetaDSLPattern, err)
	}
	cp, err := ast.Compile(pat, lang, "")
	if err != nil {
		return nil, fmt.Errorf("topology/%s: check %q: %s does not compile for %s=%q: %w", AnalyzerName, c.ID, corpus.MetaDSLPattern, corpus.MetaLanguage, lang, err)
	}
	defer cp.Close()

	// ParseWhere is strict-mode and ValidateWhereKinds rejects a kind the
	// grammar lacks BEFORE the walk, which is what keeps a typo'd where-tree
	// distinguishable from a clean zero.
	where, err := ast.ParseWhere(c.Where)
	if err != nil {
		return nil, fmt.Errorf("topology/%s: check %q: %s does not parse: %w", AnalyzerName, c.ID, corpus.MetaCheckWhere, err)
	}
	if err := ast.ValidateWhereKinds(where, lang); err != nil {
		return nil, fmt.Errorf("topology/%s: check %q: %s names a kind the %s=%q grammar does not have: %w", AnalyzerName, c.ID, corpus.MetaCheckWhere, corpus.MetaLanguage, lang, err)
	}

	// THE PARSED where MUST REACH ast.Match, AND NO SOURCE GREP CAN SEE WHETHER
	// IT DID. Parsing and validating a where-tree and then passing nil here
	// satisfies every structural gate on this file while producing a silently
	// WIDER scan in which every where-carrying check over-reports. The only
	// thing that catches it is a test comparing a narrowed check against its
	// un-narrowed twin over the same tree.
	matches, _, err := ast.Match(ctx, req.RepoRoot, lang, cp, where, ast.Scope{
		Repo:            req.Name,
		PackagePrefixes: checkPathPrefixes(req.PathPrefix),
		IncludeTests:    false,
	})
	if err != nil {
		return nil, fmt.Errorf("topology/%s: check %q: walk %s: %w", AnalyzerName, c.ID, req.Name, err)
	}

	sites := make([]foundation.Finding, 0, len(matches))
	for _, m := range matches {
		sites = append(sites, astSiteFinding(entry, sev, m))
	}
	sortSites(sites)
	return sites, nil
}

// astSiteFinding builds one finding for one match site.
//
// Evidence[0] is '<file>:<line>' — the dedup key — and it is taken from the
// match's FILESYSTEM-TRUE file path and start line rather than from any
// graph-hydrated enclosing node id, which would be stale.
func astSiteFinding(entry corpusEntry, sev foundation.Severity, m ast.RawMatch) foundation.Finding {
	site := m.FilePath + ":" + strconv.Itoa(m.StartLine)
	evidence := []string{site, entry.Check.ID}
	evidence = append(evidence, captureTexts(m)...)
	return foundation.Finding{
		Algorithm: AnalyzerName,
		Severity:  sev,
		Title:     checkTitle(entry.Node, entry.Check.ID) + " at " + site,
		Summary:   checkGuidance(entry.Node),
		Evidence:  evidence,
		Metrics:   map[string]float64{"line": float64(m.StartLine)},
		Metadata: map[string]string{
			MetaKeyFile:    m.FilePath,
			MetaKeyLine:    strconv.Itoa(m.StartLine),
			MetaKeyCheckID: entry.Check.ID,
		},
	}
}

// captureTexts renders the match's named captures as supporting evidence,
// sorted by capture name so the render is stable across runs.
func captureTexts(m ast.RawMatch) []string {
	names := make([]string, 0, len(m.Captures))
	for name := range m.Captures {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+m.Captures[name].Text)
	}
	return out
}

// checkSeverity re-resolves the check's severity against the foundation ladder.
//
// The contract's severity vocabulary IS foundation.Severity, so no translation
// is needed and none is done. AN UNMAPPABLE VALUE IS AN ERROR NAMING THE CHECK,
// NEVER A DEFAULT: defaulting would silently relabel a critical finding as info.
// This is defense in depth — ValidateFixtures takes a Check VALUE, so a caller
// may hand this package one that never passed through corpus.ParseCheck.
func checkSeverity(c corpus.Check) (foundation.Severity, error) {
	switch c.Severity {
	case foundation.SeverityInfo, foundation.SeverityNotice, foundation.SeverityWarning, foundation.SeverityCritical:
		return c.Severity, nil
	default:
		return "", fmt.Errorf("topology/%s: check %q carries %s=%q, which is not on the severity ladder (%s, %s, %s, %s)",
			AnalyzerName, c.ID, corpus.MetaSeverity, c.Severity,
			foundation.SeverityInfo, foundation.SeverityNotice, foundation.SeverityWarning, foundation.SeverityCritical)
	}
}

// checkTitle reads the check's display name off its SOURCE NODE. corpus.Check is
// the machine half and carries no prose, so a consumer that needs a title reads
// it from the node it already holds. The id is the fallback only when a corpus
// author left the node unnamed.
func checkTitle(n *knowledgev1.Node, id string) string {
	if name := strings.TrimSpace(n.GetSymbolName()); name != "" {
		return name
	}
	return id
}

// checkGuidance reads the check's prose guidance off its source node,
// preferring Description and falling back to Content.
func checkGuidance(n *knowledgev1.Node) string {
	if d := strings.TrimSpace(n.GetDescription()); d != "" {
		return d
	}
	return strings.TrimSpace(n.GetContent())
}
