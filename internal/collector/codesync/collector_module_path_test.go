// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// TestCodeCollector_Collect_PopulatesModulePath verifies the FUL-241
// Phase 5 client-side go.mod read: when a repo has a go.mod, the
// returned CollectResult.ModulePath carries the declared module path.
func TestCodeCollector_Collect_PopulatesModulePath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/probe\n\ngo 1.22\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package main

func main() { println("x") }
`)

	c := &CodeCollector{}
	result, err := c.Collect(newTestCtx(t), dir, collector.CollectOptions{})
	require.NoError(t, err)
	assert.Equal(t, "example.com/probe", result.ModulePath)
}

// TestCodeCollector_Collect_NoGoMod_EmptyModulePath verifies non-Go repos
// (those without a go.mod at the root) produce an empty ModulePath
// rather than erroring — the storesink writes an empty value through
// and the DSM analyzer skips the graph gracefully.
func TestCodeCollector_Collect_NoGoMod_EmptyModulePath(t *testing.T) {
	dir := t.TempDir()
	// A Python-only fixture with no Go module marker — collect must still
	// succeed (validateCodeRoot accepts top-level .py).
	writeFile(t, filepath.Join(dir, "main.py"), "print('hello')\n")

	c := &CodeCollector{}
	result, err := c.Collect(newTestCtx(t), dir, collector.CollectOptions{})
	require.NoError(t, err)
	assert.Empty(t, result.ModulePath)
	assert.Empty(t, result.LayerConfig)
}

// TestCodeCollector_Collect_LayerConfigPopulated verifies the FUL-241
// Phase 5 client-side .knowledge/topology_layers.yaml read: when the
// file exists, its raw body is shipped over the wire. The server side
// re-parses (ConfigFileProvider.Layers in dsm_layers.go).
func TestCodeCollector_Collect_LayerConfigPopulated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/probe\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package main

func main() {}
`)
	yamlBody := "source: hand-written\nlayers:\n  - name: base\n    packages:\n      - store\n  - name: top\n    packages:\n      - cmd\n"
	writeFile(t, filepath.Join(dir, ".knowledge", "topology_layers.yaml"), yamlBody)

	c := &CodeCollector{}
	result, err := c.Collect(newTestCtx(t), dir, collector.CollectOptions{})
	require.NoError(t, err)
	assert.Equal(t, "example.com/probe", result.ModulePath)
	assert.YAMLEq(t, yamlBody, result.LayerConfig)
}
