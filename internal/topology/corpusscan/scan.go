// SPDX-License-Identifier: Apache-2.0

// scan.go is the analyzer entry point: input validation, the run pipeline, and
// the registry self-registration that makes corpus_scan dispatchable by name.

package corpusscan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// CorpusScanAnalyzer executes fixture-validated corpus checks against a target
// code graph and its working tree.
type CorpusScanAnalyzer struct{}

// Name returns the analyzer's stable registry identifier.
func (CorpusScanAnalyzer) Name() string { return AnalyzerName }

// init self-registers the analyzer. foundation.Register panics on a duplicate
// name, so a collision is a boot panic rather than a silent shadow.
func init() { foundation.Register(CorpusScanAnalyzer{}) }

// Run executes the whole scan: read the checks corpus, probe the environment
// once, then per check re-validate its fixtures and execute it, emitting
// render-only findings.
//
// Run IS the in-process entry point. foundation.Get(AnalyzerName) returns this
// analyzer and Run drives a whole scan with no tool dispatch involved.
func (CorpusScanAnalyzer) Run(ctx context.Context, req foundation.Request) ([]foundation.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("topology/%s: %w", AnalyzerName, err)
	}
	subset, err := validateScanRequest(req)
	if err != nil {
		return nil, err
	}
	set, err := fetchCorpus(ctx, req, subset)
	if err != nil {
		return nil, err
	}
	return runCorpus(ctx, req, set)
}

// validateScanRequest refuses every malformed input with a typed error naming
// the offending value and the accepted vocabulary, and never defaults.
//
// IT DEVIATES FROM THE dsm SKELETON ON ONE POINT, DELIBERATELY. dsm returns
// (nil, nil) for a non-code graph — a silent skip that made sense when an
// all-analyzers sweep dispatched every analyzer at every graph. That sweep does
// not exist: foundation.All() is read only to build the available-analyzers
// error message and by a family-local Register wrapper, so corpus_scan is only
// ever dispatched BY NAME. A graph mismatch is therefore caller error and must
// be loud. Do not "fix" this back toward the dsm shape.
func validateScanRequest(req foundation.Request) ([]string, error) {
	if req.Graph != kgtypes.GraphCode {
		return nil, fmt.Errorf("topology/%s: graph=%q is not analyzable — corpus checks run against a code graph (%s)",
			AnalyzerName, req.Graph, kgtypes.GraphCode)
	}
	if req.Name == "" {
		return nil, fmt.Errorf("topology/%s: the target repo is required — pass repo, which rides into the analyzer as the code-graph instance name", AnalyzerName)
	}
	if req.RepoRoot == "" {
		return nil, fmt.Errorf("topology/%s: the repo working-directory root is required — ast checks walk the tree off disk and there is nothing to walk", AnalyzerName)
	}
	if req.Language == "" {
		return nil, fmt.Errorf("topology/%s: language is required — it selects the checks corpus to read and there is no default corpus", AnalyzerName)
	}
	if req.Caller == nil {
		return nil, fmt.Errorf("topology/%s: req.Caller must not be nil", AnalyzerName)
	}
	return parseChecksSubset(req.Extra)
}

// parseChecksSubset reads the ONE Extra key this analyzer consumes.
//
// It reads the RAW map value on purpose. foundation.ExtraString returns its
// default when the key is missing OR empty, and foundation.ExtraFloat /
// foundation.ExtraInt silently default on a PARSE FAILURE. Silent defaulting on
// malformed input is what the repo's bad-input rule forbids, and it is worst on
// a selector that decides WHICH checks run: a typo'd id must not quietly widen
// the scan to the whole corpus. An ABSENT key means "every check" and is
// legitimate; a present-but-unresolvable id is an error raised where the corpus
// is known, in fetchCorpus.
func parseChecksSubset(extra map[string]string) ([]string, error) {
	raw, present := extra[ExtraKeyChecks]
	if !present {
		return nil, nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("topology/%s: %s is present but empty — omit the key to scan every check", AnalyzerName, ExtraKeyChecks)
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		id := strings.TrimSpace(p)
		if id == "" {
			return nil, fmt.Errorf("topology/%s: %s element %d is empty — the value is a comma-separated list of check node ids", AnalyzerName, ExtraKeyChecks, i+1)
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
