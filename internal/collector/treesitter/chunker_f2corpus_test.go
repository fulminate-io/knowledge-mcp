// SPDX-License-Identifier: Apache-2.0

// THIS FILE IS AN EXTERNAL TEST PACKAGE, and the reason is structural rather
// than stylistic. Both instruments here have to read what the RESOLVER produced
// — IMPLEMENTS edges, their endpoints' file paths, the declared-conformance
// decline counters — and every one of those is the parser's, which imports
// treesitter. An in-package test file could not import it back. `treesitter_test`
// is the language's own escape hatch for exactly this direction, and the file
// still lives in this directory so the criteria that run
// `go test ./internal/collector/treesitter/` reach it.
package treesitter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"log/slog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// f2FixtureCorpus is the checked-in corpus both instruments default to.
const f2FixtureCorpus = "testdata/f2corpus"

// f2AuditRoot returns the corpus root: F2_AUDIT_ROOT when set, otherwise the
// checked-in fixture corpus.
//
// ONE TEST SERVES BOTH INSTRUMENTS, which is what keeps the fixture run and the
// real-source run comparable line for line. A separate test per corpus would
// drift and the two would stop measuring the same thing.
func f2AuditRoot(t *testing.T) string {
	t.Helper()
	if root := os.Getenv("F2_AUDIT_ROOT"); root != "" {
		require.DirExistsf(t, root, "F2_AUDIT_ROOT names %q, which is not a directory", root)
		return root
	}
	abs, err := filepath.Abs(f2FixtureCorpus)
	require.NoError(t, err)
	require.DirExists(t, abs, "the checked-in fixture corpus must exist")
	return abs
}

// f2LangOf maps a path to the language stratum it is reported under. It reads
// the EXTENSION rather than the corpus directory, so the same function works
// over a real checkout that has no per-language layout.
func f2LangOf(path string) treesitter.Language {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".rs":
		return treesitter.LangRust
	case ".swift":
		return treesitter.LangSwift
	case ".cpp", ".cc", ".cxx", ".hpp", ".hh":
		return treesitter.LangCPP
	case ".c":
		return treesitter.LangC
	case ".h":
		// DISCOVERY ONLY. A `.h` is decided by the PARSE rather than by its
		// extension — it reaches C first and is adopted under cpp only when the
		// C parse errors — so this answer is a placeholder used to select files
		// for the walk. Every ATTRIBUTION below reads the language the chunker
		// actually chose, through the fileLang map.
		return treesitter.LangC
	}
	return treesitter.LangUnknown
}

// f2Stratum is one language's measured boundary over a corpus.
type f2Stratum struct {
	Files            int
	Declarations     int
	QualifierSites   int
	QualifierBinds   int
	UnboundSites     int
	ConformSpellings int
	Contracts        int
	SlotBinds        map[string]int
}

// f2ConformCounters is the declared-conformance derivation's own account of
// what it declined, read off the log line the emitter already writes.
//
// THE COUNTERS ARE NOT RETURNED BY THE EMITTER — it logs them — so capturing
// the record is the only way to observe a decline REASON from outside the
// package. Reading the production log line is what makes this a measurement of
// the shipped path rather than of a reimplementation of it.
type f2ConformCounters struct {
	Supertypes         int
	TypePairs          int
	MemberPairs        int
	Unresolvable       int
	NonContract        int
	AmbiguousSupertype int
	AmbiguousMember    int
	Seen               bool
}

// f2Populate runs the production populate pass over a corpus root and returns
// its result together with the conformance counters captured from the log.
func f2Populate(t *testing.T, root string) (parser.PopulateResult, f2ConformCounters) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	res, err := parser.Populate(context.Background(), "f2audit", root)
	require.NoError(t, err)

	var counters f2ConformCounters
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if !strings.Contains(line, "declared conformance") {
			continue
		}
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		num := func(k string) int {
			if v, ok := rec[k].(float64); ok {
				return int(v)
			}
			return 0
		}
		counters = f2ConformCounters{
			Supertypes:         num("supertypes"),
			TypePairs:          num("type_pairs"),
			MemberPairs:        num("member_pairs"),
			Unresolvable:       num("unresolvable"),
			NonContract:        num("non_contract"),
			AmbiguousSupertype: num("ambiguous_supertype"),
			AmbiguousMember:    num("ambiguous_member"),
			Seen:               true,
		}
	}
	return res, counters
}

// f2ChunkStrata chunks a corpus through the production chunk path and rolls the
// per-declaration facts up by language.
func f2ChunkStrata(t *testing.T, root string) (map[treesitter.Language]*f2Stratum, map[string]treesitter.Language) {
	t.Helper()
	var files []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if f2LangOf(p) != treesitter.LangUnknown {
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return relErr
			}
			files = append(files, rel)
		}
		return nil
	}))
	require.NotEmpty(t, files, "control: the corpus walk found source files, or every count below is a vacuous zero")

	results, _, err := parser.ChunkFiles(context.Background(), root, files)
	require.NoError(t, err)

	out := map[treesitter.Language]*f2Stratum{}
	fileLang := map[string]treesitter.Language{}
	for _, res := range results {
		fileLang[res.FilePath] = res.Language
		s := out[res.Language]
		if s == nil {
			s = &f2Stratum{SlotBinds: map[string]int{}}
			out[res.Language] = s
		}
		s.Files++
		for _, ch := range res.Chunks {
			s.Declarations++
			if ch.TypeFacts != nil {
				s.ConformSpellings += len(ch.TypeFacts.Conforms)
				if ch.TypeFacts.IsInterface {
					s.Contracts++
				}
			}
		}
		// The qualifier map reaches resolution through an EDGE's reference site,
		// so it is counted there rather than off the chunk — a map the walk built
		// but no edge carried would be invisible to the ladder.
		seen := map[*treesitter.RefSite]bool{}
		for i := range res.Edges {
			ref := res.Edges[i].Ref
			if ref == nil || seen[ref] {
				continue
			}
			seen[ref] = true
			s.QualifierSites++
			if n := len(ref.QualifierTypes); n > 0 {
				s.QualifierBinds += n
			} else {
				s.UnboundSites++
			}
		}
	}
	return out, fileLang
}

// TestSystemsFixtureCorpusAudit prints each armed language's measured boundary
// over a corpus and holds it to plan-MANDATED minimums.
//
// THE PRINTED BOUNDARIES ARE THE POINT, NOT THE FLOORS. The counts are what a
// reviewer reads to see WHERE each arm stops, and they move with the corpus, so
// none of them is pinned as a literal. The floors are "at least one" minimums
// that catch a corpus which stopped being walked or quietly lost a case — never
// tree-derived counts, which would agree with whatever the tree does.
func TestSystemsFixtureCorpusAudit(t *testing.T) {
	root := f2AuditRoot(t)
	strata, fileLang := f2ChunkStrata(t, root)
	res, counters := f2Populate(t, root)

	implByMethod := map[string]int{}
	crossFile := map[treesitter.Language]int{}
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeImplements {
			continue
		}
		implByMethod[e.Method]++
		from, to := f2NodeFile(e.FromId), f2NodeFile(e.ToId)
		lang := f2AttributedLang(fileLang, from)
		if from != to {
			crossFile[lang]++
		}
		// THE SLOT-BIND STRATUM IS REPORTED PER SHAPE, so a language that
		// captured binds and emitted none of them reads as a zero a reviewer
		// can see rather than as an absent column.
		if shape, isSlot := strings.CutPrefix(e.Method, kgtypes.EdgeMethodSlotBind); isSlot {
			if s := strata[lang]; s != nil {
				s.SlotBinds[shape]++
			}
		}
	}

	t.Logf("corpus root: %s", root)
	for _, lang := range []treesitter.Language{treesitter.LangRust, treesitter.LangSwift, treesitter.LangCPP, treesitter.LangC} {
		s := strata[lang]
		if s == nil {
			t.Logf("%-6s absent from this corpus", lang)
			continue
		}
		t.Logf("%-6s files=%d declarations=%d qualifier_sites=%d qualifier_binds=%d unbound_sites=%d conform_spellings=%d contracts=%d cross_file_conformance=%d slot_binds=%v",
			lang, s.Files, s.Declarations, s.QualifierSites, s.QualifierBinds, s.UnboundSites,
			s.ConformSpellings, s.Contracts, crossFile[lang], s.SlotBinds)
	}
	t.Logf("IMPLEMENTS edges by Method: %v", implByMethod)
	t.Logf("declared-conformance counters: %+v", counters)

	// THE SURPRISE LINE. A language that CAPTURED conformance and RESOLVED none
	// of it across files is the shape worth reading this whole run for: the
	// capture arm is working and the graph still gains nothing. It is printed
	// unconditionally, on every corpus, so a real-source run cannot report it
	// quietly — and on the pinned leveldb corpus it fires for cpp, because that
	// project's public headers parse clean under the C grammar and never reach
	// the cpp query set at all.
	for _, lang := range []treesitter.Language{treesitter.LangRust, treesitter.LangSwift, treesitter.LangCPP} {
		if s := strata[lang]; s != nil && s.ConformSpellings > 0 && crossFile[lang] == 0 {
			t.Logf("SURPRISE: %s captured %d declared-conformance spellings and resolved NONE across files on this corpus",
				lang, s.ConformSpellings)
		}
	}

	// KNOWN-POSITIVE CONTROL FOR THE WHOLE RUN. Two empty sets agree perfectly,
	// so a corpus that produced nothing at all must fail here rather than pass
	// every floor below by vacuity.
	require.NotEmpty(t, strata, "control: the corpus produced at least one language stratum")
	require.True(t, counters.Seen,
		"control: the declared-conformance derivation ran and logged its counters, so the numbers above are measurements rather than zero values")

	for lang, s := range strata {
		if lang == treesitter.LangUnknown {
			continue
		}
		assert.Positivef(t, s.QualifierBinds,
			"%s is present in this corpus but its arm recorded no qualifier binding at all", lang)
	}

	if root != f2FixtureRoot(t) {
		// A FOREIGN ROOT IS MEASURED, NOT GATED, AND THE ASYMMETRY IS THE
		// DESIGN RATHER THAN A RELAXATION. The floors below assert that THIS
		// PLAN'S OWN FIXTURE CORPUS still contains the cases it was built to
		// contain — they are fixture INTEGRITY checks. A real checkout's
		// cross-file yield is a property of that project's layout and of the
		// collector's routing, which is exactly what the printed boundaries and
		// the SURPRISE line above are for. Asserting a floor over source this
		// plan does not control would turn a finding about someone else's tree
		// into a red gate that no edit here could fix.
		return
	}

	// THE CROSS-FILE FLOORS ARE SEPARATE PER LANGUAGE BECAUSE THE MECHANISMS
	// ARE: rust reaches another file through a `use` bind, cpp through an
	// include bind, swift through a module scope derived from the path. One
	// language's success cannot stand in for another's.
	for _, lang := range []treesitter.Language{treesitter.LangRust, treesitter.LangCPP, treesitter.LangSwift} {
		require.NotNilf(t, strata[lang], "the fixture corpus must carry a %s stratum", lang)
		assert.Positivef(t, strata[lang].ConformSpellings,
			"%s is present but captured no declared-conformance spelling", lang)
		assert.Positivef(t, crossFile[lang],
			"%s captured conformance but resolved none of it ACROSS FILES; a same-file corpus satisfies every other floor here", lang)
	}

	// C'S FLOOR IS PER SHAPE, because the two shapes reach their slot by
	// different routes: a designated pair names its field outright while a
	// positional element derives it from the declaration's recorded field
	// ORDER, and one working is no evidence about the other.
	cStratum := strata[treesitter.LangC]
	require.NotNil(t, cStratum, "the fixture corpus must carry a C stratum")
	for _, shape := range []string{"designated", "positional"} {
		assert.Positivef(t, cStratum.SlotBinds[shape],
			"C emitted no %s slot-bind edge; the fixture declares both shapes, so a zero here is the emitter rather than the corpus", shape)
	}
}

// f2FixtureRoot returns the checked-in fixture corpus's absolute path.
func f2FixtureRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(f2FixtureCorpus)
	require.NoError(t, err)
	return abs
}

// f2AttributedLang reports the language the CHUNKER chose for a file, falling
// back to the extension for a path it never chunked.
//
// THE DISTINCTION IS LOAD-BEARING FOR `.h`. extMap sends every header to C and
// the cpp grammar is adopted only on a parse error, so attributing a
// cross-header conformance by extension files a genuine cpp result under C —
// which is precisely the mis-attribution this function exists to stop.
func f2AttributedLang(fileLang map[string]treesitter.Language, path string) treesitter.Language {
	if lang, ok := fileLang[path]; ok && lang != "" {
		return lang
	}
	return f2LangOf(path)
}

// f2NodeFile returns the file-path half of a node ID.
func f2NodeFile(nodeID string) string {
	if i := strings.LastIndex(nodeID, ":"); i >= 0 {
		return nodeID[:i]
	}
	return nodeID
}

// TestBindOnlyNoRegression is the bind-only guarantee as an executable
// assertion: a reference the collector already resolved to a single target must
// keep that exact target once these arms register.
//
// IT DOES NOT COVER C, and the silence would otherwise read as coverage. C's own
// phase widens its Calls query and adds slot-bind edges — both of which create
// NEW references and NEW edges rather than binding existing ones — so C is
// explicitly outside this guarantee and its files are filtered out below. The C
// fixture directory still participates in the audit above.
func TestBindOnlyNoRegression(t *testing.T) {
	root := f2AuditRoot(t)

	_, fileLang := f2ChunkStrata(t, root)
	armedRes, _ := f2Populate(t, root)
	armed := f2BoundCalls(armedRes, fileLang)

	for _, lang := range []treesitter.Language{treesitter.LangRust, treesitter.LangSwift, treesitter.LangCPP} {
		treesitter.UnregisterQualifierTypes(lang)
		treesitter.UnregisterTypeFacts(lang)
	}
	// RESTORING BY RE-REGISTERING, never by deleting: an unregistered production
	// arm silently disarms the feature for every later test in the same binary,
	// and the symptom would not be a missing arm — it would be calls quietly
	// resolving through a lower rung in whatever runs next.
	t.Cleanup(func() {
		treesitter.RegisterRustQualifierTypes()
		treesitter.RegisterRustTypeFacts()
		treesitter.RegisterSwiftQualifierTypes()
		treesitter.RegisterSwiftTypeFacts()
		treesitter.RegisterCPPQualifierTypes()
		treesitter.RegisterCPPTypeFacts()
	})
	unarmedRes, _ := f2Populate(t, root)
	unarmed := f2BoundCalls(unarmedRes, fileLang)

	require.NotEmpty(t, unarmed,
		"control: the unarmed pass resolved at least one call to a single target, or the comparison below is vacuous")
	for key := range unarmed {
		assert.Containsf(t, armed, key,
			"a call the unarmed pass had already bound must keep the identical target once the arms register: %s", key)
	}
	// KNOWN-POSITIVE CONTROL for the arms having done anything: the armed pass
	// binds strictly more. Without it, arms that returned nil for everything
	// would satisfy every assertion above.
	assert.Greater(t, len(armed), len(unarmed),
		"the armed pass must bind MORE than the unarmed one, or this test proves only that nothing changed")
}

// f2BoundCalls returns the "from -> to" key of every CALLS edge resolved to a
// SINGLE target, excluding C.
//
// CONFIDENCE IS THE DISCRIMINATOR. A reference resolved to one candidate carries
// the zero value while a fan-out group divides its confidence across members, so
// comparing every edge regardless would treat NARROWING a group — the whole
// point of the typed-qualifier rung — as a broken binding.
func f2BoundCalls(res parser.PopulateResult, fileLang map[string]treesitter.Language) map[string]bool {
	out := map[string]bool{}
	for _, e := range res.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls || e.Confidence != 0 {
			continue
		}
		if f2AttributedLang(fileLang, f2NodeFile(e.FromId)) == treesitter.LangC {
			continue
		}
		out[e.FromId+" -> "+e.ToId] = true
	}
	return out
}
