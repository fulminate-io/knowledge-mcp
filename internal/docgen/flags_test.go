// SPDX-License-Identifier: Apache-2.0

package docgen

import (
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

func TestRenderFlagTable_Columns(t *testing.T) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var port int
	var root string
	var wait time.Duration
	fs.IntVar(&port, "port", graphclient.DefaultPort, "TCP port the graph server listens on")
	fs.StringVar(&root, "root", ".", "Project root directory")
	fs.DurationVar(&wait, "timeout", 30*time.Second, "Max wait for shutdown")

	out := renderFlagTable(fs)

	assert.Contains(t, out, "| Flag | Default | Description |")
	// Const-backed default resolves to the literal value via DefValue (no hardcode).
	assert.Contains(t, out, "| `--port` | `15022` | TCP port the graph server listens on |")
	assert.Contains(t, out, "| `--root` | `.` | Project root directory |")
	assert.Contains(t, out, "| `--timeout` | `30s` | Max wait for shutdown |")
}

func TestRenderFlagTable_DeterministicLexicalOrder(t *testing.T) {
	build := func() *flag.FlagSet {
		fs := flag.NewFlagSet("demo", flag.ContinueOnError)
		var a, c string
		var b int
		fs.StringVar(&c, "zeta", "", "z")
		fs.IntVar(&b, "mid", 1, "m")
		fs.StringVar(&a, "alpha", "", "a")
		return fs
	}
	first := renderFlagTable(build())
	for range 20 {
		assert.Equal(t, first, renderFlagTable(build()), "render must be byte-identical (VisitAll lexical order)")
	}
	assert.Less(t, strings.Index(first, "`--alpha`"), strings.Index(first, "`--mid`"))
	assert.Less(t, strings.Index(first, "`--mid`"), strings.Index(first, "`--zeta`"))
}

func TestRenderFlagTable_NoFlags(t *testing.T) {
	fs := flag.NewFlagSet("empty", flag.ContinueOnError)
	out := renderFlagTable(fs)
	assert.Contains(t, out, "_(no flags)_")
}
