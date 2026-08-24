// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"sort"
	"strings"
	"unicode"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// deterministicChunkTypes is the CLOSED ALLOWLIST of raw tree-sitter chunk
// types whose Summary and Keywords the collector composes itself, so they
// never enter the server's LLM summary gap set.
//
// THE CONTRACT: a chunk type absent from this map keeps today's behavior
// byte-for-byte — empty Summary, empty Keywords, and the LLM handles it.
// ADDING a type here is a deliberate act that must also update
// TestDeterministicChunkTypes_ClosedAllowlist, which pins the exact key set.
//
// CONSIDERED AND EXCLUDED, as rules rather than as a list, so a later reader
// can tell a deliberate omission from an oversight. The counts are
// ILLUSTRATIVE and TREE-DERIVED — measured at a0ce95fa over this repo's code
// graph — never fixed facts; re-derive before quoting them anywhere else.
//
//   - Control-flow and block-structured statement types are OUT because their
//     bodies are not first-line-summarizable: a lead taken from the first line
//     of an `if` or a loop describes the condition, not what the block does.
//     Measured at a0ce95fa: if_statement 40, list 31, case_statement 11,
//     for_statement 9, binary_expression 3, while_statement 2, block 1.
//
//   - The Dockerfile instruction family is OUT because the Dockerfile path
//     already has a bespoke consumer — populate.go:167-176 populates NodeFile
//     Content so the BUILDS linker can resolve without a server-side
//     filesystem read — so its chunk types deserve their own decision rather
//     than inheriting this one by default. Measured at a0ce95fa:
//     copy_instruction 12, run_instruction 10, from_instruction 7,
//     env_instruction 2, label_instruction 2, entrypoint_instruction 1;
//     34 together.
//
//   - Non-YAML config grammars are OUT of V1 because their volume is
//     negligible in the measured corpora, which makes the question not worth
//     deciding yet. block_mapping_pair below is the YAML form and is IN.
//     Measured at a0ce95fa: document 5, option 5, and table,
//     table_array_element and block at 4 or fewer each.
//
//   - Language-specific variants of an allowlisted form that V1 does not name
//     by string are OUT of V1 scope: this map keys on the RAW grammar string,
//     so a sibling spelling is a separate decision. Measured at a0ce95fa:
//     import_from_statement 15 (Python's import form; V1 names
//     import_statement only), import 1, short_var_declaration 2 (Go
//     function-local `:=`), redirected_statement 16 and pipeline 4 (bash).
//
// lexical_declaration is the HYBRID entry: allowlisted here, but eligible
// only when the chunk is NOT exported. deterministicChunkFields enforces that
// half; this map cannot express it.
var deterministicChunkTypes = map[string]bool{
	"import_statement":     true,
	"export_statement":     true,
	"block_mapping_pair":   true,
	"const_declaration":    true,
	"var_declaration":      true,
	"expression_statement": true,
	"statement":            true,
	"command":              true,
	"variable_assignment":  true,
	"test_block":           true,
	"lexical_declaration":  true,
}

// deterministicMaxContentBytes is the content-size gate: an allowlisted chunk
// larger than this keeps the LLM, because the type string alone is not a
// low-information detector. The orphan path that emits the nameless statement
// family (collector/treesitter/chunker_edges.go) applies only a LOWER size
// filter, so one raw type covers both a one-line re-export and a 78-line
// declaration. The detector is the type allowlist AND the size property.
//
// CROSS-MODULE AGREEMENT, BY MATCHING NAME AND VALUE RATHER THAN BY IMPORT.
// The server declares an identical constant in
// cmd/knowledge-server/internal/store/node_type_eligibility_table.go, where it
// gates the embed opt-out against the same rule. cmd/knowledge and
// cmd/knowledge-server are separate Go modules and AGENTS.md forbids any
// hand-written shared package outside generated protobuf, so the two agree by
// name and value. The in-tree precedent for exactly this deliberate
// duplication is authCallbackPort at cmd/frontend/proxy.go:49, whose own doc
// comment records the same reasoning. Change one and you must change the
// other; a criterion on this plan reads BOTH declarations.
const deterministicMaxContentBytes = 200

// deterministicSummaryMaxLen caps the composed Summary in bytes, so downstream
// consumers see bounded input regardless of how long one source line is.
// Precedent for a byte-length cap on a deterministic collector-composed
// summary: summaryMaxLen at cmd/knowledge/internal/collector/cloud/summarize.go:14,
// same value.
const deterministicSummaryMaxLen = 500

// hasPlaceholderShape is the client-side mirror of the server predicate
// hasPlaceholderPrefix / HasPlaceholderSummary at
// cmd/knowledge-server/internal/store/node_type_eligibility_table.go:61-65. It
// matches the same three shapes with the same operators.
//
// A summary matching any of them re-enters the server's summary gap set
// forever, because the gap predicate treats a placeholder as no summary at
// all. The collector therefore declines to claim such a node rather than
// emitting one.
//
// THE NAME AND THE PACKAGE ARE LOAD-BEARING, NOT STYLE. This mirror must not
// be named HasPlaceholderSummary and must not live in
// cmd/knowledge/internal/kgtypes: a landed criterion asserts that no
// HasPlaceholderSummary is declared there, and would go permanently red
// against otherwise-correct work.
func hasPlaceholderShape(s string) bool {
	return strings.HasPrefix(s, "Directory with ") ||
		strings.HasPrefix(s, "Git branch ") ||
		(strings.Contains(s, " file (") && strings.HasSuffix(s, " bytes)"))
}

// deterministicKeywordsMaxItems caps the composed Keywords token count. 15 is
// the maxItems of the LLM summarizer's own keywords output schema — the
// schemaJSON literal at cmd/knowledge/internal/llmproviders/summarizer_llm.go:106
// declares {"minItems":3,"maxItems":15} — so a deterministic value stays inside
// the same bound every LLM-written Keywords value in the corpus already obeys.
const deterministicKeywordsMaxItems = 15

// deterministicChunkFields composes the Summary and Keywords for one chunk,
// returning ("", "") whenever the chunk is not eligible. A caller may therefore
// write BOTH returned values unconditionally onto the node and reproduce
// today's behavior byte-for-byte for every ineligible chunk.
//
// ELIGIBILITY — all five must hold, else both returns are empty:
//
//	E1. the raw chunk type is in deterministicChunkTypes.
//	E2. lexical_declaration is eligible only when NOT exported (the hybrid
//	    rule: exported module-level declarations keep the LLM). Go's
//	    const_declaration and var_declaration are deterministic regardless of
//	    export — that is the narrower, specifically-named reading of the
//	    ticket, and it is a settled decision. If exported Go const/var should
//	    ever keep the LLM instead, this condition widens to cover those two
//	    types and the per-type test table gains two cases; nothing else moves.
//	E3. test_block is exempt from the size gate because its lead is derived
//	    from the human-written label chain rather than from source text, so
//	    body size says nothing about the summary's fidelity. Every other
//	    allowlisted type must be at or under deterministicMaxContentBytes.
//	E4. the composed summary is non-empty and is not a placeholder shape.
//	E5. the composed keywords string is non-empty — an empty Keywords reopens
//	    the server's summary gap, which tests BOTH fields, so a summary-only
//	    node would deliver zero saving while looking correct everywhere else.
//
// E4 IS ENFORCEMENT, NOT COMPENSATION. Chunk content is arbitrary source text
// this code does not control, so a composed summary CAN legitimately land on a
// placeholder shape — a one-line const whose text happens to end " bytes)" is
// a real, reachable input. The rule is that a template must NEVER emit a
// placeholder shape; declining to claim the node, and letting it fall through
// to the LLM exactly as today, is how that rule is kept. It is not a repair
// path for a state that cannot occur.
//
// The three eligibility tests that need no allocation run FIRST, so the
// overwhelmingly common case — function, method and type chunks — costs one
// map lookup and nothing else.
func deterministicChunkFields(chunk treesitter.Chunk) (summary, keywords string) {
	if !deterministicChunkTypes[chunk.ChunkType] {
		return "", ""
	}
	if chunk.ChunkType == "lexical_declaration" && chunk.Exported {
		return "", ""
	}
	if chunk.ChunkType != "test_block" && len(chunk.Content) > deterministicMaxContentBytes {
		return "", ""
	}

	// A chunk with no lead has nothing to summarize; emitting bare " — <path>"
	// would be a title with no subject, and these types render their Summary AS
	// the result title because they carry no SymbolName. Declining is the same
	// safe direction E4 takes.
	lead := deterministicLead(chunk)
	if lead == "" {
		return "", ""
	}

	summary = truncateDeterministicSummary(lead + " — " + chunk.FilePath)
	if summary == "" || hasPlaceholderShape(summary) {
		return "", ""
	}

	keywords = deterministicKeywords(chunk)
	if keywords == "" {
		return "", ""
	}
	return summary, keywords
}

// deterministicLead returns the leading clause of the composed summary.
//
// Two rules only. test_block leads with its human-written describe/it label
// chain, which IS the summary a reader wants. Every other allowlisted type
// leads with its own source text, whitespace-collapsed: source tokens are the
// signal that is MISSING from search for these nodes, because the BM25 index
// and the reranker both exclude Content for the code graph while Description
// is already indexed by both. That is also why the lead is NOT taken from the
// folded doc comment.
func deterministicLead(chunk treesitter.Chunk) string {
	if chunk.ChunkType == "test_block" {
		labelChain := chunk.Name
		if chunk.ParentName != "" {
			labelChain = chunk.ParentName + " > " + chunk.Name
		}
		return `test "` + labelChain + `"`
	}
	return collapseWS(chunk.Content)
}

// collapseWS collapses every whitespace run to a single space. A LOCAL COPY of
// the one-liner at cmd/knowledge/internal/collector/web/parse_dom_emphasis.go:126
// (collapseText), following the convention stated at
// cmd/knowledge/internal/collector/pdf/chunk/normalize.go:16-20: pulling
// collector/web in for a 1-line helper would invert the dependency layering, so
// a local copy is justified. Safe for empty input.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateDeterministicSummary caps s at deterministicSummaryMaxLen bytes.
// Byte-length truncation keeps the cap deterministic and matches len()
// semantics; a UTF-8 corner case losing one rune at the boundary is acceptable
// for embed input. Same shape and same rationale as truncateSummary at
// cmd/knowledge/internal/collector/cloud/summarize.go:73.
func truncateDeterministicSummary(s string) string {
	if len(s) <= deterministicSummaryMaxLen {
		return s
	}
	return s[:deterministicSummaryMaxLen]
}

// deterministicKeywords composes the Keywords value: lowercased, deduplicated,
// SORTED and joined by a SINGLE SPACE — the same shape every LLM-written
// Keywords value in the corpus already has (strings.Join(item.Keywords, " ") at
// cmd/knowledge/internal/llmproviders/summarizer_llm.go:156).
//
// Sorting is what makes the output reproducible across runs; nothing here may
// depend on Go map iteration order.
//
// KNOWN LIMITATION, accepted for these types: inferred vocabulary an LLM would
// add — naming a protocol or a concept the source line never spells — is not
// reproducible from fields alone and is not attempted.
func deterministicKeywords(chunk treesitter.Chunk) string {
	tokens := make([]string, 0, deterministicKeywordsMaxItems+1)
	tokens = appendIdentifierTokens(tokens, chunk.Name)
	tokens = appendIdentifierTokens(tokens, chunk.ParentName)
	tokens = append(tokens, chunk.ChunkType)
	if chunk.Language != "" {
		tokens = append(tokens, string(chunk.Language))
	}
	tokens = appendPathTokens(tokens, chunk.FilePath)

	seen := make(map[string]bool, len(tokens))
	uniq := make([]string, 0, len(tokens))
	for _, t := range tokens {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		uniq = append(uniq, t)
	}
	sort.Strings(uniq)
	if len(uniq) > deterministicKeywordsMaxItems {
		uniq = uniq[:deterministicKeywordsMaxItems]
	}
	return strings.Join(uniq, " ")
}

// appendPathTokens appends the path segments of filePath, with the file
// extension stripped from the final segment.
func appendPathTokens(dst []string, filePath string) []string {
	if filePath == "" {
		return dst
	}
	segs := strings.FieldsFunc(filePath, func(r rune) bool { return r == '/' || r == '\\' })
	for i, seg := range segs {
		if i == len(segs)-1 {
			if dot := strings.LastIndexByte(seg, '.'); dot > 0 {
				seg = seg[:dot]
			}
		}
		dst = appendIdentifierTokens(dst, seg)
	}
	return dst
}

// appendIdentifierTokens splits an identifier on snake_case, kebab-case and dot
// boundaries, then on camelCase boundaries within each part, appending each
// token. A manual scan rather than a regex: this runs once per chunk on a hot
// collector path.
func appendIdentifierTokens(dst []string, ident string) []string {
	if ident == "" {
		return dst
	}
	parts := strings.FieldsFunc(ident, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || unicode.IsSpace(r)
	})
	for _, part := range parts {
		dst = appendCamelTokens(dst, part)
	}
	return dst
}

// appendCamelTokens splits one part on lower-to-upper rune transitions.
func appendCamelTokens(dst []string, part string) []string {
	if part == "" {
		return dst
	}
	runes := []rune(part)
	start := 0
	for i := 1; i < len(runes); i++ {
		if unicode.IsUpper(runes[i]) && !unicode.IsUpper(runes[i-1]) {
			dst = append(dst, string(runes[start:i]))
			start = i
		}
	}
	return append(dst, string(runes[start:]))
}
