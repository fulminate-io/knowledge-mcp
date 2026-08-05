// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// luaConcurrencyFixture is deliberately dense in quoted strings, long-bracket
// strings and long-bracket comments: those are exactly the constructs the lua
// grammar's external scanner tracks, so they are what a corrupted scanner
// mis-lexes. It is embedded rather than read from a fixture repo so the test
// is hermetic.
const luaConcurrencyFixture = `
local _M = {}

local sep    = ", "
local prefix = "cache:"
local quote  = '"'
local apos   = "'"

local template = [[
  select * from sessions
  where token = '%s' and realm = "%s"
]]

--[==[
  A long-bracket comment containing 'single' and "double" quotes,
  plus a nested-looking ]] sequence that must not terminate it.
]==]

function _M.render(name, realm)
  local parts = { "a", 'b', "c'd", 'e"f', "g\"h" }
  local joined = table.concat(parts, sep)
  return string.format("%s%s@%s [%s]", prefix, name, realm, joined)
end

function _M.classify(kind)
  if kind == "read" then
    return "ro", 'replica'
  elseif kind == 'write' then
    return "rw", "primary"
  elseif kind == "admin" then
    return [[su]], [==[root]==]
  end
  return "unknown", "none"
end

function _M.escape(s)
  s = string.gsub(s, "'", "''")
  s = string.gsub(s, quote, quote .. quote)
  s = string.gsub(s, "\\n", " ")
  return s
end

local handlers = {
  ["get"]    = function(k) return _M.render(k, "get") end,
  ["set"]    = function(k) return _M.render(k, 'set') end,
  ["delete"] = function(k) return _M.render(k, "delete") end,
}

function _M.dispatch(verb, key)
  local h = handlers[verb]
  if not h then
    error("no handler for " .. verb .. " on " .. tostring(key))
  end
  return h(key), template, apos
end

return _M
`

// parseLuaBaseline returns the serial, uncontended parse of the fixture. A
// serial lua parse is a fixed point — the scanner globals cannot be disturbed
// when only one parse is in flight — so this is the correct oracle.
func parseLuaBaseline(t *testing.T, src []byte) string {
	t.Helper()

	p := NewParser()
	defer p.Close()

	tree, err := p.Parse(context.Background(), src, LangLua)
	require.NoError(t, err)
	defer tree.Close()

	root := tree.RootNode()
	sexp := root.String()

	// Known-positive controls on the oracle itself. Without these, an empty or
	// trivial baseline would make the divergence count below vacuously zero.
	require.False(t, root.HasError(), "fixture must parse cleanly when serial; root=%s", root.Type())
	require.Greater(t, root.ChildCount(), uint32(5), "baseline tree is too shallow to be a meaningful oracle")
	require.Greater(t, len(sexp), 1000, "baseline s-expression is too small to be a meaningful oracle")

	return sexp
}

// TestParseLuaConcurrentMatchesSerialBaseline is the regression test for the
// lua serialization in Parse. Before that serialization existed this failed
// hard: N goroutines parsing identical bytes with their own parsers returned
// structurally different trees, because the vendored lua grammar keeps its
// scanner state in process-global C variables.
func TestParseLuaConcurrentMatchesSerialBaseline(t *testing.T) {
	src := []byte(luaConcurrencyFixture)
	baseline := parseLuaBaseline(t, src)

	// Positive control for the comparator: a materially different source must
	// produce a different s-expression. This proves the equality check below
	// can actually observe divergence, so a zero divergent count means "no
	// corruption" rather than "the comparison never discriminates".
	// Prepended, not appended: `return _M` must stay the final statement of the
	// chunk or the variant would not be valid lua.
	mutated := parseLuaBaseline(t, []byte("local extra = \"head\"\n"+luaConcurrencyFixture))
	require.NotEqual(t, baseline, mutated, "comparator cannot distinguish different lua sources")

	const (
		goroutines = 8
		iterations = 25
	)

	var (
		divergent atomic.Int64
		parses    atomic.Int64
		parseErrs atomic.Int64
		wg        sync.WaitGroup
	)

	for range goroutines {
		wg.Go(func() {
			p := NewParser()
			defer p.Close()

			for range iterations {
				tree, err := p.Parse(context.Background(), src, LangLua)
				if err != nil {
					parseErrs.Add(1)
					continue
				}
				parses.Add(1)
				if tree.RootNode().String() != baseline {
					divergent.Add(1)
				}
				tree.Close()
			}
		})
	}
	wg.Wait()

	// The zero asserted below is only meaningful if every parse actually ran.
	require.Zero(t, parseErrs.Load(), "lua parses returned errors")
	require.Equal(t, int64(goroutines*iterations), parses.Load(), "not every planned parse completed")

	assert.Zero(t, divergent.Load(),
		"%d of %d concurrent lua parses produced a tree differing from the serial baseline — "+
			"lua parsing is not serialized against the grammar's process-global scanner state",
		divergent.Load(), parses.Load())
}

// TestChunkFileLuaConcurrentMatchesSerialBaseline covers the same defect one
// layer up, at the surface the code indexer actually uses: concurrent
// Chunker.ChunkFile calls over identical lua bytes must yield identical chunk
// and edge counts. A corrupted parse silently drops chunks and edges here with
// no error and no skip, which is how nondeterministically incomplete lua
// symbols reach a code graph.
func TestChunkFileLuaConcurrentMatchesSerialBaseline(t *testing.T) {
	src := []byte(luaConcurrencyFixture)
	const path = "lib/concurrency_fixture.lua"

	serial := func() (int, int) {
		c := NewChunker()
		defer c.Close()
		res, err := c.ChunkFile(context.Background(), path, src)
		require.NoError(t, err)
		return len(res.Chunks), len(res.Edges)
	}

	wantChunks, wantEdges := serial()

	// Known-positive control: the fixture must actually produce work, otherwise
	// an all-zeros agreement below would prove nothing.
	require.Positive(t, wantChunks, "fixture yields no chunks")
	require.Positive(t, wantEdges, "fixture yields no edges")

	// And the serial path itself must be a fixed point.
	gotChunks, gotEdges := serial()
	require.Equal(t, wantChunks, gotChunks, "serial chunking is not deterministic")
	require.Equal(t, wantEdges, gotEdges, "serial chunking is not deterministic")

	const (
		goroutines = 8
		iterations = 10
	)

	var (
		mu       sync.Mutex
		mismatch []string
		runs     atomic.Int64
		wg       sync.WaitGroup
	)

	for range goroutines {
		wg.Go(func() {
			c := NewChunker()
			defer c.Close()

			for range iterations {
				res, err := c.ChunkFile(context.Background(), path, src)
				if err != nil {
					mu.Lock()
					mismatch = append(mismatch, "chunk error: "+err.Error())
					mu.Unlock()
					continue
				}
				runs.Add(1)
				if len(res.Chunks) != wantChunks || len(res.Edges) != wantEdges {
					mu.Lock()
					mismatch = append(mismatch, fmt.Sprintf("(%dc/%de)", len(res.Chunks), len(res.Edges)))
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()

	require.Equal(t, int64(goroutines*iterations), runs.Load(), "not every planned chunk run completed")
	assert.Empty(t, mismatch,
		"concurrent lua chunking diverged from the serial baseline (%d chunks / %d edges); observed: %s",
		wantChunks, wantEdges, strings.Join(mismatch, " "))
}
