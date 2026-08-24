// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// This file holds the F3 corpus harness's MEASUREMENT MACHINERY — the
// projection, the arm-off and counting registrations, and the report. The
// assertions that consume it live beside it in f3_corpus_test.go. The split is
// the repo's 500-line file cap, and it falls on the seam between what the
// harness MEASURES and what it CLAIMS, which is the seam worth splitting on
// anyway.

// f3Languages are the languages this ticket arms. The list drives the arm-off
// baseline as well as the per-language projections, so a language added to one
// cannot be forgotten in the other.
var f3Languages = []treesitter.Language{
	treesitter.LangTypeScript,
	treesitter.LangTSX,
	treesitter.LangJavaScript,
	treesitter.LangPython,
	treesitter.LangRuby,
	treesitter.LangElixir,
}

// f3Measurement is one Populate's projection: what the arms produced, by
// language, by kind and by file boundary.
//
// EVERY FIELD IS A MEASUREMENT AND NONE IS A FLOOR. The tree these numbers come
// from moves — sibling per-language tickets, the shared mechanism, and any
// corpus refresh all change them — so no value here is written into a criterion
// as a pinned literal. The only assertions this file makes are structural ones:
// wrong targets are zero, the instrument is live, and the fixture control binds.
type f3Measurement struct {
	callsTotal int

	// typedByLang counts CALLS edges the typed-qualifier rung decided, per the
	// CALLER's language.
	typedByLang map[string]int
	// typedSameFile and typedCrossFile split those by whether the target lives
	// in the caller's own file. This is the file-boundary column; the ROUTE
	// column is resolvedByRoute below.
	typedSameFile  map[string]int
	typedCrossFile map[string]int

	// conformByKind counts emitted IMPLEMENTS edges by the declared clause kind
	// carried in Method — the four Table 1 kinds this ticket reaches.
	conformByKind map[string]int
	// conformSameFile and conformCrossFile split the TYPE-level conformance
	// edges by file boundary, which is the column that shows whether imported
	// supertypes resolved.
	conformSameFile  int
	conformCrossFile int

	// offeredDirect and offeredFromCall count the BINDINGS THE ARMS OFFERED, by
	// route, counted in a wrapper around each registered arm.
	//
	// THEY ARE THE SUPPLY SIDE AND ARE NOT THE ROUTE SPLIT. They answer how much
	// the arms PROPOSED; resolvedByRoute below answers how much survived
	// resolution and by which mechanism. Read together the pair shows the
	// attrition between the two, which is why both are kept — but the offered
	// numbers are a weak proxy for the resolved ones and must never be reported
	// as them: measured on the agent corpus, the arms offered 8,791 bindings
	// while the rung resolved 9,798 references across all armed languages, and
	// the two populations are not even the same denominator.
	offeredDirect   int
	offeredFromCall int

	// resolvedByRoute is the RESOLVED-by-route split, keyed "<language>/<route>"
	// and read off the resolution pass's own log line. It is the number the
	// acceptance record asks for: which MECHANISM reached each answer, which no
	// edge carries and no other counter separates. resolvedTotal is the rung's
	// undifferentiated bind count, kept beside it so the split can be checked to
	// account for every bind rather than merely to look plausible.
	resolvedByRoute map[string]int
	resolvedTotal   int

	// The declared-conformance emitter's own counters, captured from its log
	// line because they are unrecoverable from the edge set: an outcome that
	// emits nothing leaves no trace in Edges by construction.
	conformSupertypes   int
	conformTypePairs    int
	conformMemberPairs  int
	conformEmitted      int
	conformUnresolvable int
	conformNonContract  int
	conformAmbiguous    int
}

func newF3Measurement() *f3Measurement {
	return &f3Measurement{
		resolvedByRoute: map[string]int{},
		typedByLang:     map[string]int{},
		typedSameFile:   map[string]int{},
		typedCrossFile:  map[string]int{},
		conformByKind:   map[string]int{},
	}
}

// conformanceLogCapture reads the declared-conformance counters off the
// emitter's slog record.
//
// THE LOG LINE IS THE ONLY SOURCE FOR THREE OF THEM. Unresolvable, NonContract
// and AmbiguousSupertype all describe supertypes that emitted NOTHING, so the
// edge set cannot distinguish them from a declaration that named no supertype at
// all — which is exactly the argument the counters' own doc comment makes for
// keeping them separate.
//
// IT MUST NOT DELEGATE TO THE HANDLER IT REPLACED, and the reason is a deadlock
// rather than a preference. slog's DEFAULT handler emits through the log
// package, and the log package's writer emits back through whatever handler is
// currently the slog default — which, once this one is installed, is this one.
// The cycle self-deadlocks on log's own mutex on the first record written, and
// it presents as a hung test rather than as a crash. Delegating to a handler
// that writes to the stream DIRECTLY breaks it.
type conformanceLogCapture struct {
	slog.Handler
	mu sync.Mutex
	m  *f3Measurement
}

func (c *conformanceLogCapture) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "collector: reference resolution" {
		c.mu.Lock()
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "typed_qualifier_binds" {
				c.m.resolvedTotal = int(a.Value.Int64())
			}
			return true
		})
		c.mu.Unlock()
	}
	if r.Message == "collector: typed-qualifier routes" {
		// THE WHOLE FAMILY IS SELECTED BY PREFIX rather than by a fixed key
		// list: the per-language keys are as wide as the corpus's language set,
		// so an enumeration here would silently drop whichever language nobody
		// thought to name.
		c.mu.Lock()
		r.Attrs(func(a slog.Attr) bool {
			if key, ok := strings.CutPrefix(a.Key, "tq_route_"); ok {
				c.m.resolvedByRoute[key] = int(a.Value.Int64())
			}
			return true
		})
		c.mu.Unlock()
	}
	if r.Message == "collector: declared conformance" {
		c.mu.Lock()
		r.Attrs(func(a slog.Attr) bool {
			n := int(a.Value.Int64())
			switch a.Key {
			case "supertypes":
				c.m.conformSupertypes = n
			case "type_pairs":
				c.m.conformTypePairs = n
			case "member_pairs":
				c.m.conformMemberPairs = n
			case "edges":
				c.m.conformEmitted = n
			case "unresolvable":
				c.m.conformUnresolvable = n
			case "non_contract":
				c.m.conformNonContract = n
			case "ambiguous_supertype":
				c.m.conformAmbiguous = n
			}
			return true
		})
		c.mu.Unlock()
	}
	return c.Handler.Handle(ctx, r)
}

// countingQualifierArms wraps each registered F3 qualifier arm so the bindings
// it OFFERS are counted by route, and returns a restore func.
func countingQualifierArms(t *testing.T, m *f3Measurement) func() {
	t.Helper()
	var mu sync.Mutex
	for _, lang := range f3Languages {
		inner, ok := treesitter.QualifierTypesArm(lang)
		if !ok {
			continue
		}
		treesitter.RegisterQualifierTypes(lang, func(n *sitter.Node, src []byte) map[string]treesitter.QualType {
			out := inner(n, src)
			mu.Lock()
			for _, qt := range out {
				if qt.FromCall {
					m.offeredFromCall++
				} else {
					m.offeredDirect++
				}
			}
			mu.Unlock()
			return out
		})
	}
	return restoreF3Arms
}

// restoreF3Arms puts every production F3 arm back.
//
// IT RESTORES RATHER THAN UNREGISTERS, which is the contract the registry's own
// doc comments spell out: Unregister DELETES rather than parks, so a cleanup
// that merely unregistered would disarm these languages for every later test in
// the same binary.
func restoreF3Arms() {
	treesitter.RegisterECMAQualifierTypes()
	treesitter.RegisterECMATypeFacts()
	treesitter.RegisterPythonQualifierTypes()
	treesitter.RegisterPythonTypeFacts()
	treesitter.RegisterRubyQualifierTypes()
	treesitter.RegisterRubyTypeFacts()
	treesitter.RegisterElixirTypeFacts()
}

// disarmF3 removes every F3 arm, for the arm-off baseline, and returns a
// restore func.
//
// WHAT IT CANNOT TAKE OUT, stated because it bounds the wrong-target claim: the
// two TypeScript QUERY changes this ticket also makes — interface members
// becoming declarations, and abstract classes becoming named — are in the query
// set, not in a registry, so the arm-off tree below still carries them. The
// baseline therefore isolates THE ARMS, which is what the bind-only rule is
// about; the query changes add declaration NODES and are measured by the node
// counts rather than by this comparison.
func disarmF3(t *testing.T) func() {
	t.Helper()
	for _, lang := range f3Languages {
		treesitter.UnregisterQualifierTypes(lang)
		treesitter.UnregisterTypeFacts(lang)
	}
	return restoreF3Arms
}

// callGroups projects a populate result into the plan's GROUP shape: a caller
// together with the BARE NAME of each target it reaches.
//
// THE BARE NAME IS WHAT MAKES THE COMPARISON MEANINGFUL. A bind-only rung may
// NARROW a group — replacing three candidate targets named `write` with the one
// the annotation picked — or leave it alone. It may never introduce a target the
// unarmed tree did not offer, and grouping by name is what lets a narrowing be
// told apart from a substitution.
func callGroups(pop parser.PopulateResult) map[[2]string]map[string]bool {
	out, _ := callGroupsWithMethods(pop)
	return out
}

// callGroupsWithMethods is callGroups plus the Method each (caller, target) edge
// carries, which is what lets a narrowing be checked for attribution.
func callGroupsWithMethods(
	pop parser.PopulateResult,
) (map[[2]string]map[string]bool, map[[2]string]string) {
	methods := map[[2]string]string{}
	out := map[[2]string]map[string]bool{}
	for _, e := range pop.Edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeCalls {
			methods[[2]string{e.FromId, e.ToId}] = e.Method
		}
	}
	for _, e := range pop.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls {
			continue
		}
		key := [2]string{e.FromId, bareTargetName(e.ToId)}
		if out[key] == nil {
			out[key] = map[string]bool{}
		}
		out[key][e.ToId] = true
	}
	return out, methods
}

// bareTargetName is the last dot-separated segment of a node ID's symbol half.
func bareTargetName(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		id = id[i+1:]
	}
	if i := strings.LastIndex(id, "."); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// measureF3 runs one Populate under the counting arms and projects it.
func measureF3(t *testing.T, dir, label string) (parser.PopulateResult, *f3Measurement) {
	t.Helper()
	m := newF3Measurement()

	prev := slog.Default()
	direct := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(&conformanceLogCapture{Handler: direct, m: m}))
	restore := countingQualifierArms(t, m)
	defer func() {
		restore()
		slog.SetDefault(prev)
	}()

	pop, err := parser.Populate(t.Context(), label, dir)
	require.NoError(t, err)

	idx := buildNodeIndex(pop.Nodes)
	conformPrefix := kgtypes.EdgeMethodDeclaredConformance
	for _, e := range pop.Edges {
		switch kgtypes.EdgeType(e.Type) {
		case kgtypes.EdgeCalls:
			m.callsTotal++
			if e.Method != string(parser.RuleTypedQualifier) {
				continue
			}
			lang := idx.langByID[e.FromId]
			m.typedByLang[lang]++
			if idx.fileByID[e.FromId] == idx.fileByID[e.ToId] {
				m.typedSameFile[lang]++
			} else {
				m.typedCrossFile[lang]++
			}
		case kgtypes.EdgeImplements:
			if !strings.HasPrefix(e.Method, conformPrefix) {
				continue
			}
			m.conformByKind[strings.TrimPrefix(e.Method, conformPrefix)]++
			// TYPE-LEVEL ONLY for the file split: a member edge shares its
			// parent's boundary by construction, so counting both would double
			// every pair and say nothing extra.
			if !strings.Contains(bareOwner(e.FromId), ".") {
				if idx.fileByID[e.FromId] == idx.fileByID[e.ToId] {
					m.conformSameFile++
				} else {
					m.conformCrossFile++
				}
			}
		}
	}
	return pop, m
}

// bareOwner is a node ID's symbol half, used to tell a type-level endpoint from
// a member-level one.
func bareOwner(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// report logs every measurement, sorted, so the -v output is the record the
// completion note is transcribed from.
func (m *f3Measurement) report(t *testing.T, label string) {
	t.Helper()
	t.Logf("[%s] CALLS total=%d", label, m.callsTotal)
	for _, lang := range f3SortedKeys(m.typedByLang) {
		t.Logf("[%s] typed-qualifier binds lang=%s total=%d same_file=%d cross_file=%d",
			label, lang, m.typedByLang[lang], m.typedSameFile[lang], m.typedCrossFile[lang])
	}
	for _, kind := range f3SortedKeys(m.conformByKind) {
		t.Logf("[%s] declared-conformance edges kind=%s count=%d", label, kind, m.conformByKind[kind])
	}
	t.Logf("[%s] declared-conformance type-level same_file=%d cross_file=%d",
		label, m.conformSameFile, m.conformCrossFile)
	t.Logf("[%s] conformance counters supertypes=%d type_pairs=%d member_pairs=%d emitted_edges=%d unresolvable=%d non_contract=%d ambiguous_supertype=%d",
		label, m.conformSupertypes, m.conformTypePairs, m.conformMemberPairs, m.conformEmitted,
		m.conformUnresolvable, m.conformNonContract, m.conformAmbiguous)
	// THE ACCOUNTING IS PRINTED AS AN ACCOUNTING, so a partial enumeration shows
	// up as arithmetic that does not close rather than as a plausible list.
	//
	// IT BALANCES AGAINST TYPE PAIRS, NEVER AGAINST EDGES. Every supertype SEEN
	// becomes exactly one of four outcomes — a resolved type pair, unresolvable,
	// non-contract, or ambiguous — while the EDGE count also carries one edge per
	// paired MEMBER beneath each type pair, so an accounting written against
	// edges over-counts by the member fan-out and never closes.
	accounted := m.conformTypePairs + m.conformUnresolvable + m.conformNonContract + m.conformAmbiguous
	t.Logf("[%s] conformance accounting: %d accounted of %d supertypes seen (type pairs + unresolvable + non_contract + ambiguous)",
		label, accounted, m.conformSupertypes)
	// THE NUMBER THE ACCEPTANCE RECORD ASKS FOR: binds that actually RESOLVED,
	// by the mechanism that reached them. The three differ in cross-file reach —
	// only the direct-type route resolves through the import binds — so a single
	// total would hide which one moved.
	routeTotal := 0
	for _, key := range f3SortedKeys(m.resolvedByRoute) {
		t.Logf("[%s] typed-qualifier binds RESOLVED %s=%d", label, key, m.resolvedByRoute[key])
		routeTotal += m.resolvedByRoute[key]
	}
	// THE SPLIT IS PRINTED AGAINST THE RUNG'S OWN TOTAL, so a route the entry
	// failed to classify shows as arithmetic that does not close rather than as
	// a set of numbers that each look reasonable.
	t.Logf("[%s] route accounting: %d accounted of %d typed-qualifier binds",
		label, routeTotal, m.resolvedTotal)

	// The OFFERED counters are kept beside the resolved ones because they cost
	// nothing and answer a DIFFERENT question — how much the arms proposed
	// against how much survived resolution — but they are not the split above,
	// and they are labeled so no reader substitutes one for the other.
	t.Logf("[%s] qualifier bindings OFFERED by route (supply side, NOT resolved): direct_type=%d from_call=%d",
		label, m.offeredDirect, m.offeredFromCall)
}

func f3SortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestF3CorpusMeasurement is this ticket's ACCEPTANCE MEASUREMENT: it reports
// what the per-language arms produced over both frozen corpora, and asserts the
// three structural properties that must hold whatever those numbers are.
//
// ITS COUNTS ARE MEASUREMENTS OF A MOVING TREE, NOT INVARIANTS. The sibling
// per-language tickets, the shared mechanism ticket and any refresh of the
// frozen corpora all change them, so no number this test prints is pinned in any
// criterion, and a future reader should re-derive rather than compare.
//
// CORPORA ARE READ-ONLY. Nothing here writes under the corpus root; see the
// corpora MANIFEST.
//
//	F3_CORPUS_ROOT=$HOME/.knowledge/tsparity go test ./internal/collector/codesync \
//	  -run '^TestF3CorpusMeasurement$' -v -count=1 -timeout 3600s
