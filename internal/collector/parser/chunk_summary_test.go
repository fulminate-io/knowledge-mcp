// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// perTypeCase is one deterministic-composition expectation. wantSummary is the
// EXACT composed Summary; an empty wantSummary means the chunk is expected to
// be declined, both fields empty.
type perTypeCase struct {
	name        string
	chunk       treesitter.Chunk
	wantSummary string
}

// overCapContent is longer than deterministicMaxContentBytes, modeled on the
// real 78-line nameless export_statement at web/e2e/fixtures/auth.fixture.ts
// that motivated the size gate: one raw chunk type covers both a one-line
// re-export and a large declaration, so the type string alone cannot be the
// low-information detector.
var overCapContent = "export const authFixture = { " + strings.Repeat("field: value, ", 20) + "};"

// deterministicPerTypeCases is shared by the per-type table test and by the
// placeholder guard's sub-assertion C, so the guard covers every case the
// table pins rather than a second, drifting list.
func deterministicPerTypeCases() []perTypeCase {
	return []perTypeCase{
		{
			name:        "import_statement",
			chunk:       treesitter.Chunk{ChunkType: "import_statement", Content: "import { renderApp } from './app'", FilePath: "web/src/main.ts", Language: treesitter.LangTypeScript},
			wantSummary: "import { renderApp } from './app' — web/src/main.ts",
		},
		{
			name:        "export_statement",
			chunk:       treesitter.Chunk{ChunkType: "export_statement", Content: "export { authFixture };", FilePath: "web/e2e/fixtures/auth.fixture.ts", Language: treesitter.LangTypeScript},
			wantSummary: "export { authFixture }; — web/e2e/fixtures/auth.fixture.ts",
		},
		{
			// Extra internal whitespace pins the whitespace-collapse rule.
			name:        "block_mapping_pair",
			chunk:       treesitter.Chunk{ChunkType: "block_mapping_pair", Content: "image:    postgres:16", FilePath: "deploy/compose.yaml", Language: treesitter.LangYaml, Name: "image"},
			wantSummary: "image: postgres:16 — deploy/compose.yaml",
		},
		{
			name:        "const_declaration",
			chunk:       treesitter.Chunk{ChunkType: "const_declaration", Content: "const maxRetries = 5", FilePath: "cmd/knowledge/internal/retry/backoff.go", Language: treesitter.LangGo, Name: "maxRetries"},
			wantSummary: "const maxRetries = 5 — cmd/knowledge/internal/retry/backoff.go",
		},
		{
			// Exported Go const/var stay deterministic — only lexical_declaration
			// is gated on export.
			name:        "const_declaration exported stays deterministic",
			chunk:       treesitter.Chunk{ChunkType: "const_declaration", Content: "const MaxRetries = 5", FilePath: "cmd/knowledge/internal/retry/backoff.go", Language: treesitter.LangGo, Name: "MaxRetries", Exported: true},
			wantSummary: "const MaxRetries = 5 — cmd/knowledge/internal/retry/backoff.go",
		},
		{
			name:        "var_declaration",
			chunk:       treesitter.Chunk{ChunkType: "var_declaration", Content: "var defaultTimeout = 30 * time.Second", FilePath: "cmd/knowledge/internal/config/timeouts.go", Language: treesitter.LangGo, Name: "defaultTimeout"},
			wantSummary: "var defaultTimeout = 30 * time.Second — cmd/knowledge/internal/config/timeouts.go",
		},
		{
			name:        "expression_statement",
			chunk:       treesitter.Chunk{ChunkType: "expression_statement", Content: "await page.goto('/login')", FilePath: "web/e2e/login.spec.ts", Language: treesitter.LangTypeScript},
			wantSummary: "await page.goto('/login') — web/e2e/login.spec.ts",
		},
		{
			// A newline in the source pins the collapse across lines.
			name:        "statement",
			chunk:       treesitter.Chunk{ChunkType: "statement", Content: "set -euo\n  pipefail", FilePath: "scripts/deploy.sh", Language: treesitter.LangBash},
			wantSummary: "set -euo pipefail — scripts/deploy.sh",
		},
		{
			name:        "command",
			chunk:       treesitter.Chunk{ChunkType: "command", Content: "go build ./...", FilePath: "scripts/build.sh", Language: treesitter.LangBash},
			wantSummary: "go build ./... — scripts/build.sh",
		},
		{
			name:        "variable_assignment",
			chunk:       treesitter.Chunk{ChunkType: "variable_assignment", Content: "ROOT=$(git rev-parse --show-toplevel)", FilePath: "scripts/env.sh", Language: treesitter.LangBash, Name: "ROOT"},
			wantSummary: "ROOT=$(git rev-parse --show-toplevel) — scripts/env.sh",
		},
		{
			name:        "test_block with parent label chain",
			chunk:       treesitter.Chunk{ChunkType: "test_block", Content: overCapContent, FilePath: "web/e2e/login.spec.ts", Language: treesitter.LangTypeScript, Name: "redirects to the dashboard", ParentName: "login flow"},
			wantSummary: "test \"login flow > redirects to the dashboard\" — web/e2e/login.spec.ts",
		},
		{
			name:        "test_block without parent",
			chunk:       treesitter.Chunk{ChunkType: "test_block", Content: overCapContent, FilePath: "web/e2e/home.spec.ts", Language: treesitter.LangTypeScript, Name: "renders the landing page"},
			wantSummary: "test \"renders the landing page\" — web/e2e/home.spec.ts",
		},
		{
			name:        "lexical_declaration not exported",
			chunk:       treesitter.Chunk{ChunkType: "lexical_declaration", Content: "const rows = await db.query(sql)", FilePath: "web/src/db/rows.ts", Language: treesitter.LangTypeScript, Name: "rows"},
			wantSummary: "const rows = await db.query(sql) — web/src/db/rows.ts",
		},
		{
			name:        "lexical_declaration exported keeps the LLM",
			chunk:       treesitter.Chunk{ChunkType: "lexical_declaration", Content: "export const apiBase = '/api/v1'", FilePath: "web/src/api/base.ts", Language: treesitter.LangTypeScript, Name: "apiBase", Exported: true},
			wantSummary: "",
		},
		{
			name:        "allowlisted type over the size cap keeps the LLM",
			chunk:       treesitter.Chunk{ChunkType: "export_statement", Content: overCapContent, FilePath: "web/e2e/fixtures/auth.fixture.ts", Language: treesitter.LangTypeScript},
			wantSummary: "",
		},
		{
			name:        "non-allowlisted type is never claimed",
			chunk:       treesitter.Chunk{ChunkType: "function_declaration", Content: "func Sync() error { return nil }", FilePath: "cmd/knowledge/internal/codesync/sync.go", Language: treesitter.LangGo, Name: "Sync"},
			wantSummary: "",
		},
	}
}

// TestDeterministicChunkSummary_PerType pins the EXACT Summary for every V1
// type plus the hybrid, size and over-reach fences. Keywords is asserted
// non-empty on every claimed case: an empty Keywords reopens the server's
// summary gap, which tests BOTH fields, so a Summary-only node would deliver
// zero saving while looking correct in every display surface.
func TestDeterministicChunkSummary_PerType(t *testing.T) {
	for _, tc := range deterministicPerTypeCases() {
		t.Run(tc.name, func(t *testing.T) {
			summary, keywords := deterministicChunkFields(tc.chunk)
			require.Equal(t, tc.wantSummary, summary)
			if tc.wantSummary == "" {
				require.Empty(t, keywords, "a declined chunk must return BOTH fields empty")
				return
			}
			require.NotEmpty(t, keywords)
			// The separator is a single space, matching the shape every
			// LLM-written Keywords value in the corpus already has.
			require.NotContains(t, keywords, "  ")
			require.Equal(t, strings.ToLower(keywords), keywords, "keywords are lowercased")
			toks := strings.Fields(keywords)
			require.LessOrEqual(t, len(toks), deterministicKeywordsMaxItems)
			require.True(t, sort.StringsAreSorted(toks), "keywords must be sorted for reproducibility")
		})
	}
}

// TestDeterministicChunkSummary_NeverPlaceholder is the guard that keeps a
// composed summary out of the three legacy placeholder shapes. A node whose
// Summary matches one re-enters the summary gap set forever, so the composer
// must decline rather than emit.
//
// It carries its own KNOWN-POSITIVE: without sub-assertion A, a
// hasPlaceholderShape that always returned false would leave B and C green and
// the guard inert.
func TestDeterministicChunkSummary_NeverPlaceholder(t *testing.T) {
	t.Run("A known-positive: the predicate fires on all three shapes", func(t *testing.T) {
		require.True(t, hasPlaceholderShape("Directory with 3 files"))
		require.True(t, hasPlaceholderShape("Git branch main"))
		require.True(t, hasPlaceholderShape("auth.go file (1234 bytes)"))
		// Control: an ordinary composed summary is not a placeholder.
		require.False(t, hasPlaceholderShape("const maxRetries = 5 — cmd/knowledge/internal/retry/backoff.go"))
	})

	t.Run("B a chunk whose summary would be a placeholder is declined", func(t *testing.T) {
		// Chunk content is arbitrary source text the composer does not
		// control, so this is enforcement of the never-emit rule, not repair
		// of a state that cannot occur.
		bad := treesitter.Chunk{ChunkType: "command", Content: "Directory with 3 files", FilePath: "scripts/list.sh", Language: treesitter.LangBash}
		require.True(t, hasPlaceholderShape(collapseWS(bad.Content)+" — "+bad.FilePath),
			"control: this fixture must actually compose into a placeholder shape")
		summary, keywords := deterministicChunkFields(bad)
		require.Empty(t, summary)
		require.Empty(t, keywords)
	})

	t.Run("C no per-type case composes a placeholder", func(t *testing.T) {
		claimed := 0
		for _, tc := range deterministicPerTypeCases() {
			summary, _ := deterministicChunkFields(tc.chunk)
			if summary == "" {
				continue
			}
			claimed++
			require.False(t, hasPlaceholderShape(summary), "case %q composed a placeholder shape: %q", tc.name, summary)
		}
		// Known-positive for C: a table in which nothing was claimed would
		// satisfy the loop above vacuously.
		require.Positive(t, claimed, "control: no case produced a summary at all")
	})
}

// TestDeterministicChunkTypes_ClosedAllowlist pins the allowlist at exactly the
// V1 entries. Adding a 12th type to the map without a deliberate edit here
// fails, which is the closed-allowlist discipline.
func TestDeterministicChunkTypes_ClosedAllowlist(t *testing.T) {
	want := []string{
		"block_mapping_pair",
		"command",
		"const_declaration",
		"export_statement",
		"expression_statement",
		"import_statement",
		"lexical_declaration",
		"statement",
		"test_block",
		"var_declaration",
		"variable_assignment",
	}
	got := make([]string, 0, len(deterministicChunkTypes))
	for k, v := range deterministicChunkTypes {
		require.True(t, v, "every allowlist entry must be true, %q is not", k)
		got = append(got, k)
	}
	sort.Strings(got)
	require.Equal(t, want, got)
	require.Len(t, want, 11)
}

// pinnedDeterministicCompositionDigest freezes the collector's DETERMINISTIC
// COMPOSITION SURFACE — the three cap constants by value, the allowlist key set,
// and the composed Summary/Keywords for every case in deterministicPerTypeCases.
//
// WHY A COUPLED PIN EXISTS AT ALL. The per-file contribution hash that drives
// incremental collect deliberately EXCLUDES Summary and Keywords (they are a
// mixed column — the server durably persists LLM-written values — so including
// them would mark every enriched file changed on every collect, forever). The
// price of that exclusion is that a change to THIS surface changes the fields
// emitted for many nodes while moving none of the 14 hashed ones: under diff mode
// those files read UNCHANGED and never re-upload, and shadow mode and the
// equivalence comparator are equally blind, because none of the three reads these
// columns. docs/collect-contribution-hash.md section C states the resulting rule;
// this pin is what makes an author notice they have triggered it.
//
// WHAT IT COVERS AND WHAT IT DOES NOT — stated exactly, because "it goes red the
// moment any of the surface moves" is FALSE as a blanket claim:
//
//	COVERED: the three cap constants BY VALUE (a cap change that declines no
//	fixture case would otherwise move nothing); the allowlist KEY SET (no fixture
//	case is an out-of-V1 candidate, so a WIDENING would otherwise move nothing);
//	and the composed values for every pinned case.
//	NOT COVERED: the three shapes hasPlaceholderShape matches; the truncation
//	BRANCH in truncateDeterministicSummary (the cap value is hashed, the branch is
//	not); and the size-gate COMPARISON at the deterministicMaxContentBytes
//	boundary (the value is hashed, > vs >= is not). The key set has a second,
//	independent gate in TestDeterministicChunkTypes_ClosedAllowlist.
//
// That residue NOW HAS AN AUTOMATED GATE of its own:
// TestCollectorOutputIdentity_Digest digests the collector's FULL emitted payload
// over the 22-language corpus, so a change to any emitted value — including the
// Summary and Keywords this pin composes — moves it. The failure message below
// names CollectorOutputVersion, that gate's carrier, rather than only the pin:
// the two are one obligation, and a change to this surface owes the same bump.
const pinnedDeterministicCompositionDigest = "f678bb315bc88f8e15dfcdd5e102a996f1bdeb2b78951faf975d316f79389025"

// TestDeterministicComposition_SurfaceDigest is the coupled-axis guard for the
// contribution hash's Summary/Keywords exclusion. See the pin's doc above for the
// exact coverage boundary.
func TestDeterministicComposition_SurfaceDigest(t *testing.T) {
	types := make([]string, 0, len(deterministicChunkTypes))
	for k := range deterministicChunkTypes {
		types = append(types, k)
	}
	// Sorted so map iteration order cannot make the digest flap.
	sort.Strings(types)

	var b strings.Builder
	b.WriteString("maxContentBytes=" + strconv.Itoa(deterministicMaxContentBytes) + "\n")
	b.WriteString("summaryMaxLen=" + strconv.Itoa(deterministicSummaryMaxLen) + "\n")
	b.WriteString("keywordsMaxItems=" + strconv.Itoa(deterministicKeywordsMaxItems) + "\n")
	b.WriteString("chunkTypes=" + strings.Join(types, ",") + "\n")
	for _, tc := range deterministicPerTypeCases() {
		summary, keywords := deterministicChunkFields(tc.chunk)
		b.WriteString(tc.name + "|" + summary + "|" + keywords + "\n")
	}

	sum := sha256.Sum256([]byte(b.String()))
	got := hex.EncodeToString(sum[:])
	if got != pinnedDeterministicCompositionDigest {
		t.Fatalf("deterministic composition surface digest = %s, pinned = %s.\n"+
			"The collector's DETERMINISTIC COMPOSITION SURFACE changed. The per-file "+
			"contribution hash cannot see Summary or Keywords, so under diff mode the "+
			"affected files read UNCHANGED and never re-upload. In the SAME change you "+
			"MUST: (1) bump CollectorOutputVersion in package parser "+
			"(collector_output_version.go) — the client-only carrier for a change to "+
			"what the collector EMITS, whose own gate is "+
			"TestCollectorOutputIdentity_Digest — which routes through the "+
			"collector-version fallback and costs exactly one full re-land per graph; "+
			"and (2) update "+
			"pinnedDeterministicCompositionDigest to %s.", got, pinnedDeterministicCompositionDigest, got)
	}
}
