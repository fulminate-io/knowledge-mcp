// SPDX-License-Identifier: Apache-2.0

package docgen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/bootstrap"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// Generate populates every managed block under guidesRoot from the live tool +
// flag catalogs, in place:
//
//	(1) for each tool in tools.AllToolSchemas(), open
//	    guidesRoot/tools/<tool.Name>.md and splice renderParamsTable(tool) into
//	    its `params` named block;
//	(2) for each client subcommand FlagSet (bootstrap.DocFlagSets), splice
//	    renderFlagTable into the corresponding named block in
//	    guidesRoot/binaries.md.
//
// Generated content lands only between the named markers; hand-written prose is
// preserved byte-for-byte. Any missing scaffold file or missing marker aborts
// loudly with a wrapped error naming the file (and, for tool files, the tool) —
// so a future tool/flag addition without a matching scaffold produces an
// actionable CI drift failure rather than a silent no-op.
func Generate(guidesRoot string) error {
	if err := generateToolParamBlocks(guidesRoot); err != nil {
		return err
	}
	return generateFlagBlocks(guidesRoot)
}

// generateToolParamBlocks splices the parameter table for every catalog tool
// into its per-tool scaffold file.
func generateToolParamBlocks(guidesRoot string) error {
	for _, tool := range tools.AllToolSchemas() {
		path := filepath.Join(guidesRoot, "tools", tool.Name+".md")
		content, err := os.ReadFile(path) //nolint:gosec // path derived from the static tool catalog, not user input
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("docgen: scaffold file %s for tool %q is missing — add it (with a `params` generated marker) so the docs stay in sync with the tool catalog", path, tool.Name)
			}
			return fmt.Errorf("docgen: read %s: %w", path, err)
		}
		next, err := spliceManagedBlock(string(content), "params", renderParamsTable(tool))
		if err != nil {
			return fmt.Errorf("docgen: splice params block for tool %q (%s): %w", tool.Name, path, err)
		}
		if next == string(content) {
			continue
		}
		if err := os.WriteFile(path, []byte(next), 0o644); err != nil { //nolint:gosec // user-readable docs, 0644 is correct
			return fmt.Errorf("docgen: write %s: %w", path, err)
		}
	}
	return nil
}

// generateFlagBlocks splices each client subcommand's flag table into its named
// block in binaries.md. Every block named by DocFlagSets must exist in the file
// (loud error otherwise).
func generateFlagBlocks(guidesRoot string) error {
	path := filepath.Join(guidesRoot, "binaries.md")
	content, err := os.ReadFile(path) //nolint:gosec // static path under the guides root
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("docgen: scaffold file %s is missing — add it with the per-subcommand flag generated markers", path)
		}
		return fmt.Errorf("docgen: read %s: %w", path, err)
	}

	out := string(content)
	for _, dfs := range bootstrap.DocFlagSets() {
		next, err := spliceManagedBlock(out, dfs.BlockName, renderFlagTable(dfs.FlagSet))
		if err != nil {
			return fmt.Errorf("docgen: splice flag block %q (%s): %w", dfs.BlockName, path, err)
		}
		out = next
	}
	if out == string(content) {
		return nil
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil { //nolint:gosec // user-readable docs, 0644 is correct
		return fmt.Errorf("docgen: write %s: %w", path, err)
	}
	return nil
}
