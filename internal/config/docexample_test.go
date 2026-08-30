// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"strings"
	"testing"
)

// guidePath is the shipped configuration guide, relative to this package.
const guidePath = "../../../../docs/guides/config.md"

// wantGuideTOMLBlocks is an EXTERNAL expectation, hand-pinned rather than
// derived from the same walk that produces the observation — a count compared
// against itself would pass on an extractor that found nothing.
//
// Derivation, recorded so a future editor can re-derive it instead of
// guessing: `grep -c '^```toml$' docs/guides/config.md` at the time the
// [embedder]/[reranker] sections were written returned 6 — one LLM
// provider/model example, four embedding/rerank examples, and one
// [credentials] block. ADDING an example to the guide is expected to fail
// this assertion; bump the constant in the same change.
const wantGuideTOMLBlocks = 6

// TestGuideTOMLExamplesParse runs the REAL parser over every ```toml block in
// the configuration guide.
//
// An example that does not parse is a defect: the guide's whole value is that
// a reader can copy a block into ~/.knowledge/config and have the daemon read
// it. Parse is the exact function the daemon reaches through config.Load at
// startup, so a green here means the daemon would accept the block.
//
// SCOPE, stated so a later reader does not over-read a pass: this asserts the
// blocks PARSE. It does NOT assert that the provider named in a block can be
// constructed, because parsing and construction are separate gates — an arm
// states its own dtype capability at construction time and the parser never
// consults it. The guide's own arm table is what documents which provider
// serves which dtype; whether every registered arm is constructible at some
// admitted dtype is asserted by
// embed.TestEveryRegisteredProvider_ConstructsAtSomeAdmittedDtype, not here.
func TestGuideTOMLExamplesParse(t *testing.T) {
	blocks := tomlBlocksFromGuide(t)

	if len(blocks) != wantGuideTOMLBlocks {
		t.Fatalf("extracted %d toml blocks from %s; want %d — if you added or removed a guide example, update wantGuideTOMLBlocks in the same change",
			len(blocks), guidePath, wantGuideTOMLBlocks)
	}

	for i, block := range blocks {
		if _, err := Parse([]byte(block.body)); err != nil {
			t.Errorf("guide toml block #%d (starting line %d) does not parse: %v\n---\n%s\n---",
				i+1, block.startLine, err, block.body)
		}
	}
}

// TestGuideTOMLExtractorRejectsBadTOML is the known-negative control for the
// test above. Without it, a Parse that silently accepted anything — or an
// extractor handing back empty strings — would be indistinguishable from a
// guide whose examples are all correct.
func TestGuideTOMLExtractorRejectsBadTOML(t *testing.T) {
	// Malformed on purpose: an unterminated string.
	if _, err := Parse([]byte("[embedder]\nprovider = \"voyage\n")); err == nil {
		t.Fatal("Parse accepted malformed TOML; the doc-example test's pass signal is worthless")
	}
	// Well-formed TOML the config layer must still refuse: a dimension outside
	// the accepted set (256/512/1024/2048). Proves the guide examples clear the
	// ADMISSION gate, not just the TOML grammar.
	if _, err := Parse([]byte("[embedder]\nprovider = \"voyage\"\ndimension = 300\n")); err == nil {
		t.Fatal("Parse accepted dimension = 300; the accepted-width refusal is not firing")
	}
}

// guideTOMLBlock is one fenced example plus where it starts, so a failure
// names a line an editor can open.
type guideTOMLBlock struct {
	body      string
	startLine int
}

func (b guideTOMLBlock) String() string { return b.body }

// tomlBlocksFromGuide extracts every ```toml fenced block from the guide.
func tomlBlocksFromGuide(t *testing.T) []guideTOMLBlock {
	t.Helper()
	data, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read %s: %v", guidePath, err)
	}
	var (
		out     []guideTOMLBlock
		cur     []string
		inBlock bool
		start   int
	)
	for i, line := range strings.Split(string(data), "\n") {
		switch {
		case !inBlock && line == "```toml":
			inBlock, cur, start = true, nil, i+1
		case inBlock && line == "```":
			out = append(out, guideTOMLBlock{body: strings.Join(cur, "\n"), startLine: start})
			inBlock = false
		case inBlock:
			cur = append(cur, line)
		}
	}
	if inBlock {
		t.Fatalf("%s: unterminated ```toml fence starting at line %d", guidePath, start)
	}
	return out
}
