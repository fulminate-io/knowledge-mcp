// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guideLink is the shipped configuration guide, READ THROUGH THIS PACKAGE'S OWN
// testdata LINK.
//
// THE LINK IS A TEST-CACHE FENCE, and it REPLACES a walk-up resolver that could
// not be one. `go test` keys a package's stored result on the files a run opened
// and DROPS any opened name that does not resolve inside the tested package's
// own module root. The previous helper walked up to the first ancestor carrying
// both a go.mod and docs/guides/config.md, which here is the REPO root — above
// this module — so the guide was never in this package's key: adding a ```toml
// example to it and re-running returned `ok (cached)`, a stored PASS for the one
// guard whose whole subject is the guide's examples. A name under this package's
// own testdata is inside cmd/knowledge, so the go tool records it, and os.Stat
// follows the link to the target's size and modification time.
//
// THE LINK'S DEPTH DIFFERS IN THE PUBLISHED MIRROR — five levels below the repo
// root here, three below the mirror root there, because the sync script maps
// cmd/knowledge/internal to internal/ — so scripts/sync-to-oss.sh re-points it.
// That mapping is exactly what made a fixed ".." count wrong in both layouts and
// sent the previous author to a walk; the link plus the re-point is the form
// that is correct in both AND visible to the cache.
const guideLink = "testdata/config-guide.md"

// guideTarget is what the link must resolve to. Asserted separately, and never
// used to READ: resolving the link first names the repo-root path again, which
// is outside this module, and undoes the fence.
const guideTarget = "docs/guides/config.md"

// guidePath is the name the failure messages print. It is the LINK, not the
// resolved target, because the link is what the read actually opened.
func guidePath(t testing.TB) string {
	t.Helper()
	return guideLink
}

// TestGuideLinkResolves is the PIN on the fence. A checkout without symlink
// support materializes the link as a one-line text stub, the extractor would
// then find zero fenced blocks, and the count assertion above would fail on
// something that is about the checkout rather than about the guide. The mutation
// that must turn this red is replacing the link with a regular file holding the
// same bytes: the copy extracts fine and this package goes straight back to
// being cacheable against a document it never re-read.
func TestGuideLinkResolves(t *testing.T) {
	info, err := os.Lstat(guideLink)
	if err != nil {
		t.Fatalf("%s must exist — it is what puts the guide in this package's test-cache key: %v", guideLink, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink (a checkout without symlink support materializes it as a text stub); "+
			"this package would then be cacheable against a guide it never read", guideLink)
	}
	// Abs BEFORE EvalSymlinks: a relative name yields a relative result, and the
	// suffix check would compare the link's own target text.
	abs, err := filepath.Abs(guideLink)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		t.Fatalf("%s must resolve; a dangling link fences nothing: %v", guideLink, err)
	}
	if !strings.HasSuffix(filepath.ToSlash(resolved), guideTarget) {
		t.Fatalf("%s resolves to %s, which is not the shipped configuration guide", guideLink, resolved)
	}
	// KNOWN POSITIVE: it serves the real guide, not an empty stub.
	body, err := os.ReadFile(guideLink)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "```toml") {
		t.Fatalf("%s serves no fenced toml example, so the extractor above would measure nothing", guideLink)
	}
}

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
			len(blocks), guidePath(t), wantGuideTOMLBlocks)
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
	path := guidePath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
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
		t.Fatalf("%s: unterminated ```toml fence starting at line %d", path, start)
	}
	return out
}
