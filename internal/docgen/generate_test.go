// SPDX-License-Identifier: Apache-2.0

package docgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/bootstrap"
	"github.com/fulminate-io/knowledge-mcp/internal/tools"
)

// scaffoldGuides builds a minimal in-temp guides tree: one tools/<name>.md per
// catalog tool (each with a params marker pair) and a binaries.md carrying every
// DocFlagSets block marker. Mirrors what Phase 4 scaffolds, so Generate has real
// markers to splice into.
func scaffoldGuides(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "tools"), 0o750))

	for _, tool := range tools.AllToolSchemas() {
		body := "# " + tool.Name + "\n\nintro prose\n\n" +
			"<!-- BEGIN GENERATED: params -->\n<!-- END GENERATED: params -->\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, "tools", tool.Name+".md"), []byte(body), 0o600))
	}

	var b strings.Builder
	b.WriteString("# Binaries\n\n")
	for _, dfs := range bootstrap.DocFlagSets() {
		b.WriteString("## " + dfs.BlockName + "\n\n")
		b.WriteString("<!-- BEGIN GENERATED: " + dfs.BlockName + " -->\n<!-- END GENERATED: " + dfs.BlockName + " -->\n\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "binaries.md"), []byte(b.String()), 0o600))
	return root
}

func TestGenerate_PopulatesEveryBlock(t *testing.T) {
	root := scaffoldGuides(t)
	require.NoError(t, Generate(root))

	// Every tool file has a populated params table.
	for _, tool := range tools.AllToolSchemas() {
		data, err := os.ReadFile(filepath.Join(root, "tools", tool.Name+".md"))
		require.NoError(t, err)
		assert.Contains(t, string(data), "| Parameter | Type | Required | Enum | Description |",
			"tool %q params block not populated", tool.Name)
	}

	// binaries.md flag blocks populated; const-backed default resolved literally.
	bin, err := os.ReadFile(filepath.Join(root, "binaries.md"))
	require.NoError(t, err)
	assert.Contains(t, string(bin), "| `--http-port` | `15023` |", "serve --http-port default must resolve to the const value")
	assert.Contains(t, string(bin), "| `--port` | `15022` |", "--port default must resolve to graphclient.DefaultPort")
	assert.Contains(t, string(bin), "| `--timeout` | `30s` |", "stop --timeout default must resolve")
}

func TestGenerate_Idempotent(t *testing.T) {
	root := scaffoldGuides(t)
	require.NoError(t, Generate(root))
	snapshot := readAll(t, root)
	require.NoError(t, Generate(root))
	assert.Equal(t, snapshot, readAll(t, root), "a second Generate must produce zero diff")
}

func TestGenerate_MissingToolFileErrorsLoudly(t *testing.T) {
	root := scaffoldGuides(t)
	// Remove one tool's scaffold file.
	victim := tools.AllToolSchemas()[0].Name
	require.NoError(t, os.Remove(filepath.Join(root, "tools", victim+".md")))

	err := Generate(root)
	require.Error(t, err, "a missing per-tool scaffold must abort Generate")
	assert.Contains(t, err.Error(), victim, "error must name the tool the missing file belongs to")
	assert.Contains(t, err.Error(), victim+".md", "error must name the missing file")
}

func TestGenerate_MissingMarkerErrorsLoudly(t *testing.T) {
	root := scaffoldGuides(t)
	// Blank a tool file's markers.
	victim := tools.AllToolSchemas()[0].Name
	require.NoError(t, os.WriteFile(filepath.Join(root, "tools", victim+".md"), []byte("# "+victim+"\n\nno markers here\n"), 0o600))

	err := Generate(root)
	require.Error(t, err, "a missing marker must abort Generate")
	assert.Contains(t, err.Error(), "BEGIN marker")
}

func TestGenerate_MissingBinariesFileErrorsLoudly(t *testing.T) {
	root := scaffoldGuides(t)
	require.NoError(t, os.Remove(filepath.Join(root, "binaries.md")))
	err := Generate(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "binaries.md")
}

func readAll(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(p) //nolint:gosec // test temp tree
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = string(data)
		return nil
	})
	require.NoError(t, err)
	return out
}
