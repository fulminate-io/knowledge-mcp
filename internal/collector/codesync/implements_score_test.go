// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// implCorpusExpect carries one corpus's locked controls and acceptance floors.
//
// THE FLOORS ARE PER-CORPUS, AND A SINGLE FLOOR WOULD BE UNSATISFIABLE. The
// measured signature-matching precision differs by corpus — 96.1% on knowledge
// against 91.1% on agent — so one 95% floor fails agent against a CORRECT
// implementation. The per-corpus table is not an optional extra borrowed from
// the sibling harness; it is the part of that harness's structure that makes it
// correct, because its floors differ per corpus exactly as its measurements do.
//
// oracleRows is the same KNOWN-POSITIVE CONTROL r2tExpect's oracleSize is: a
// translation that silently drops most of the oracle scores everything against a
// shrunken truth and looks excellent.
type implCorpusExpect struct {
	oracleRows     int     // frozen TSV cardinality control
	precisionFloor float64 // vs the translated go/types oracle
	vtaRecallFloor float64 // vs the VTA-witnessed set
	hitRateFloor   float64 // interface-decl CALLS targeting
}

// EVERY FLOOR IS MEASURED-MINUS-FIVE, on the ticket's 5-point tolerance
// convention — the same convention the sibling harness's floors use:
//
//	knowledge  precision 96.1 → 91.1   vta_recall 97.3 → 92.3   hit_rate 83.7 → 78.7
//	agent      precision 91.1 → 86.1   vta_recall 99.7 → 94.7   hit_rate 89.4 → 84.4
//
// The FLOORS are plan-mandated literals and belong here. THE MEASUREMENTS THEY
// DERIVE FROM ARE NOT: everything the harness prints is corpus-derived,
// reported, and never asserted against a remembered number.
var implExpect = map[string]implCorpusExpect{
	"knowledge": {oracleRows: 3397, precisionFloor: 91.1, vtaRecallFloor: 92.3, hitRateFloor: 78.7},
	"agent":     {oracleRows: 886, precisionFloor: 86.1, vtaRecallFloor: 94.7, hitRateFloor: 84.4},
}

// implTruthTSV names each corpus's frozen go/types ground truth.
var implTruthTSV = map[string]string{
	"knowledge": "k_impl_truth.tsv",
	"agent":     "a_impl_truth.tsv",
}

// implSitesTSV names each corpus's frozen VTA site dump, whose fire-iface rows
// carry the concrete receivers VTA actually dispatched to through an interface.
var implSitesTSV = map[string]string{
	"knowledge": "k_out/sites.tsv",
	"agent":     "a_out/sites.tsv",
}

// implOracleSep is the separator the frozen oracle uses between a repo-relative
// directory and a type name. A directory path and a Go identifier can neither of
// them contain a NUL.
const implOracleSep = "\x00"

// TestImplementsParityScore is this work's ACCEPTANCE MEASUREMENT: it answers
// "did the derivation meet the measured floors", once, against the frozen
// corpora and the frozen oracles. It is NOT a standing regression gate — it is a
// whole-system aggregate, so an individual guard could regress and be absorbed
// by it without moving a floor. The standing gates are the per-rule subtests.
//
//	IMPL_ROOT=$HOME/.knowledge/tsparity go test ./internal/collector/codesync/ \
//	  -run '^TestImplementsParityScore$' -v -count=1 -timeout 3600s
func TestImplementsParityScore(t *testing.T) {
	root := os.Getenv("IMPL_ROOT")
	if root == "" {
		t.Skip("set IMPL_ROOT=<tsparity root> to run the IMPLEMENTS parity scoring")
	}
	// An operator-supplied path flowing into this test's own reads and writes.
	root = filepath.Clean(root)

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	evidence := filepath.Join(home, ".knowledge", "treesitter-parity-evidence", "fanout")

	// SERIALLY, no t.Parallel: each subtest is a whole-repo Populate whose cost
	// is dominated by memory, and two concurrent runs would contend for it with
	// no wall-clock win.
	for _, label := range []string{"knowledge", "agent"} {
		t.Run(label, func(t *testing.T) {
			scoreImplementsCorpus(t, root, evidence, label)
		})
	}
}

func scoreImplementsCorpus(t *testing.T, root, evidence, label string) {
	t.Helper()
	expect, ok := implExpect[label]
	require.True(t, ok, "no locked controls for corpus %q", label)

	corpusDir := filepath.Join(root, "corpora", label)
	postDir := filepath.Join(root, "post", label)

	pop, err := parser.Populate(t.Context(), filepath.Base(corpusDir), corpusDir)
	require.NoError(t, err)

	// TYPE-LEVEL EDGES ONLY are scored, because the oracle is a type-to-type
	// relation. The method-level edges are a projection of the same pairs and
	// scoring them again would double-count each satisfaction.
	typeNodes, ifaceNodes := implTypeNodeMaps(pop.Nodes)
	require.NotEmpty(t, typeNodes, "control: the corpus declared type nodes at all")

	derived := map[pair]bool{}
	for _, e := range pop.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeImplements {
			continue
		}
		if _, isType := typeNodes[e.FromId]; !isType {
			continue // a method-level edge
		}
		derived[pair{from: typeNodes[e.FromId], to: typeNodes[e.ToId]}] = true
	}

	// THE KEY TRANSLATION IS THE MOST LIKELY DEFECT, so it is controlled before
	// anything is scored. The oracle keys a repo-relative DIRECTORY plus a type
	// name; a node ID is "<file>:<Name>". The mapping is derived from the
	// populate result's OWN nodes — file path's directory plus SymbolName — never
	// by string-munging node IDs.
	oracleRows := readTSVRows(t, filepath.Join(evidence, implTruthTSV[label]))
	require.Lenf(t, oracleRows, expect.oracleRows,
		"want: the locked frozen-oracle cardinality for %s\ngot:  %d against locked %d — the frozen file changed, and every downstream number would be scored against the wrong truth",
		label, len(oracleRows), expect.oracleRows)

	declaredKeys := map[string]bool{}
	for id := range typeNodes {
		declaredKeys[typeNodes[id]] = true
	}
	oracle := map[pair]bool{}
	var untranslated []string
	for _, cols := range oracleRows {
		if len(cols) < 2 {
			continue
		}
		if !declaredKeys[cols[0]] || !declaredKeys[cols[1]] {
			// RESIDUE IS REPORTED, NEVER SILENT: a row naming a type the collector
			// demonstrably never chunked cannot be scored either way.
			untranslated = append(untranslated, strings.Join(cols, "\t"))
			continue
		}
		oracle[pair{from: cols[0], to: cols[1]}] = true
	}
	require.NotEmpty(t, oracle,
		"control: the translated oracle is non-empty — a translation that dropped everything would score against a shrunken truth and look excellent")

	tp, fp := 0, 0
	var fpDetail []string
	for p := range derived {
		if oracle[p] {
			tp++
			continue
		}
		fp++
		fpDetail = append(fpDetail, p.from+"\t"+p.to)
	}
	fn := 0
	var fnDetail []string
	for p := range oracle {
		if !derived[p] {
			fn++
			fnDetail = append(fnDetail, p.from+"\t"+p.to)
		}
	}

	sites := implFireIfaceSites(t, filepath.Join(evidence, implSitesTSV[label]))
	vtaHit, vtaTotal, vtaUnindexed := implVTARecall(sites, derived, ifaceNodes)
	hits, denom := implTargetingHitRate(pop.Edges, ifaceNodes, sites)
	oneIfaces, onePairs := implOneMethodCensus(pop.Edges, typeNodes)

	precision := pct(tp, len(derived))
	vtaRecall := pct(vtaHit, vtaTotal)
	hitRate := pct(hits, denom)

	// THE LOCKED LINE, starting at column zero with a prefix no diagnostic in
	// this file uses. Diagnostics use want:/got: and never this prefix.
	fmt.Printf("IMPLSCORE corpus=%s interfaces=%d derived_pairs=%d tp=%d fp=%d fn=%d "+
		"precision_pct=%.1f recall_pct=%.1f vta_recall_pct=%.1f vta_denominator=%d "+
		"vta_denominator_unindexed=%d "+
		"hit_rate_pct=%.1f hit_rate_denominator=%d one_method_interfaces=%d one_method_pairs=%d "+
		"oracle_rows=%d oracle_translated=%d oracle_untranslated=%d\n",
		label, len(ifaceNodes), len(derived), tp, fp, fn,
		precision, pct(tp, len(oracle)), vtaRecall, vtaTotal,
		vtaUnindexed,
		hitRate, denom, oneIfaces, onePairs,
		len(oracleRows), len(oracle), len(untranslated))

	// Per-pair detail, so a false positive can be READ rather than inferred.
	writeDetail(t, postDir, "implements_fp.tsv", fpDetail)
	writeDetail(t, postDir, "implements_fn.tsv", fnDetail)
	writeDetail(t, postDir, "implements_oracle_untranslated.tsv", untranslated)

	// THE WHOLE DERIVED SET, in the frozen prototype's own two-column shape, so a
	// divergence in the derived TOTAL can be reconciled pair-by-pair against
	// <corpus>_out/implements_derived_sig.tsv rather than inferred from a count.
	// A count difference says only that the two instruments disagree; this says
	// about WHICH pair, which is the only form in which the disagreement can be
	// explained or dismissed.
	derivedDetail := make([]string, 0, len(derived))
	for p := range derived {
		derivedDetail = append(derivedDetail, p.from+"\t"+p.to)
	}
	sort.Strings(derivedDetail)
	writeDetail(t, postDir, "implements_derived.tsv", derivedDetail)

	// VACUITY CONTROLS, BEFORE the floor assertions. A perfect precision score is
	// also exactly what a harness that derived nothing produces.
	require.NotEmptyf(t, derived, "want: a non-empty derived set for %s\ngot:  zero pairs — the scoring would be vacuous", label)
	require.Positivef(t, vtaTotal, "want: a non-empty VTA-witnessed set for %s\ngot:  zero — the recall would be vacuous", label)
	// THE DENOMINATOR'S CARDINALITY CONTROL. vtaUnindexed is the count of rows
	// this instrument holds and the frozen prototype's recall loop excludes, so
	// it must stay a SMALL fraction of the denominator. A large value means the
	// two instruments are no longer measuring the same population and the
	// comparison to the frozen figures has quietly stopped being one.
	require.Lessf(t, vtaUnindexed, vtaTotal/10,
		"want: the divergence from the frozen instrument's denominator stays marginal for %s\ngot:  %d of %d rows name an interface this collector never indexed",
		label, vtaUnindexed, vtaTotal)
	require.Positivef(t, denom, "want: a non-empty interface-targeting population for %s\ngot:  zero — the hit rate would be vacuous", label)

	require.GreaterOrEqualf(t, precision, expect.precisionFloor,
		"want: precision at or above the measured floor for %s\ngot:  %.1f against %.1f — see %s",
		label, precision, expect.precisionFloor, filepath.Join(postDir, "implements_fp.tsv"))
	require.GreaterOrEqualf(t, vtaRecall, expect.vtaRecallFloor,
		"want: VTA-witnessed recall at or above the measured floor for %s\ngot:  %.1f against %.1f",
		label, vtaRecall, expect.vtaRecallFloor)
	require.GreaterOrEqualf(t, hitRate, expect.hitRateFloor,
		"want: interface-decl targeting hit rate at or above the measured floor for %s\ngot:  %.1f against %.1f over %d sites",
		label, hitRate, expect.hitRateFloor, denom)
}

// implTypeNodeMaps returns node ID → "<relDir>\x00<Name>" for every TYPE
// declaration, plus the subset that are interface declarations.
//
// AN INTERFACE IS IDENTIFIED BY ITS OWN METHOD-SPEC CHILDREN rather than by a
// flag on the node: a method_elem chunk's node ID is "<file>:<Iface>.<Method>",
// so a type declaration with at least one such child IS an interface. That keeps
// the harness reading only what the graph actually publishes.
func implTypeNodeMaps(nodes []*knowledgev1.Node) (typeNodes, ifaceNodes map[string]string) {
	typeNodes = make(map[string]string, len(nodes))
	ifaceNodes = make(map[string]string, len(nodes))
	specParents := map[string]bool{}
	for _, n := range nodes {
		if n.Id == "" || n.FilePath == "" || n.SymbolName == "" {
			continue
		}
		switch n.Type {
		case "type_declaration":
			dir := filepath.ToSlash(filepath.Dir(filepath.ToSlash(n.FilePath)))
			typeNodes[n.Id] = dir + implOracleSep + n.SymbolName
		case "method_elem":
			// The enclosing interface's node ID is this node's ID with the
			// ".<Method>" suffix removed.
			if i := strings.LastIndex(n.Id, "."); i > 0 {
				specParents[n.Id[:i]] = true
			}
		}
	}
	for id, key := range typeNodes {
		if specParents[id] {
			ifaceNodes[id] = key
		}
	}
	return typeNodes, ifaceNodes
}

// implFireIfaceSite is one interface-dispatch call site the frozen VTA run
// witnessed: the calling declaration, the method name, the interface its
// qualifier's type resolved to, and the concrete receivers VTA dispatched to.
type implFireIfaceSite struct {
	caller   string
	name     string
	ifaceKey string
	oracle   []string
}

// implFireIfaceSites reads the frozen VTA site dump and returns one entry per
// DISTINCT (caller, name) interface-dispatch group.
//
// THE ORACLE COLUMN IS PIPE-JOINED, not comma-joined — the dump writes
// strings.Join(r.Oracle, "|"). Splitting on the wrong separator does not fail
// loudly: a multi-target cell parses as one unusable string while single-target
// cells still work, so the measurement quietly shrinks to the sites that happen
// to have exactly one target. Measured before the fix: a 1,048-row denominator
// against the 4,752 the frozen report states.
func implFireIfaceSites(t *testing.T, sitesPath string) []implFireIfaceSite {
	t.Helper()
	rows := readTSVRows(t, sitesPath)
	require.NotEmpty(t, rows, "control: the frozen VTA site dump is non-empty")

	col := map[string]int{}
	for i, name := range rows[0] {
		col[name] = i
	}
	for _, need := range []string{"caller", "name", "type", "type_dir", "verdict", "oracle"} {
		_, ok := col[need]
		require.Truef(t, ok, "control: the site dump carries a %q column", need)
	}

	var out []implFireIfaceSite
	seen := map[string]bool{}
	for _, cols := range rows[1:] {
		if len(cols) <= col["oracle"] {
			continue
		}
		if !strings.HasPrefix(cols[col["verdict"]], "fire-iface") || cols[col["type_dir"]] == "" {
			continue
		}
		gk := cols[col["caller"]] + implOracleSep + cols[col["name"]]
		if seen[gk] {
			continue
		}
		seen[gk] = true
		var oracle []string
		for o := range strings.SplitSeq(cols[col["oracle"]], "|") {
			if o = strings.TrimSpace(o); o != "" {
				oracle = append(oracle, o)
			}
		}
		out = append(out, implFireIfaceSite{
			caller:   cols[col["caller"]],
			name:     cols[col["name"]],
			ifaceKey: cols[col["type_dir"]] + implOracleSep + cols[col["type"]],
			oracle:   oracle,
		})
	}
	require.NotEmpty(t, out, "control: the dump holds interface-dispatch sites at all")
	return out
}

// implVTARecall scores the derivation against the concrete receivers VTA
// actually dispatched to through an interface.
//
// VTA IS A WITNESS, NOT A TRUTH: it only sees dispatches that occur, so a pair
// it never witnessed is not evidence of a false positive. It is used here only
// for RECALL, which is the direction a witness can support.
//
// THE DENOMINATOR DIVERGES FROM THE FROZEN PROTOTYPE, KNOWINGLY, AND THE
// DIFFERENCE IS RETURNED RATHER THAN LEFT TO BE REDISCOVERED. The prototype
// (~/.knowledge/treesitter-parity-evidence/fanout/analyze/implements.go, the
// recall loop at :210) drops a dispatch group whose interface is absent from its
// OWN indexed interface set — `if _, ok := ifaces[ik]; !ok { continue }` — and
// this function does not. Read first-hand in that file, not taken from a report.
//
// KEEPING THE WIDER DENOMINATOR IS THE CONSERVATIVE CHOICE and that is why it is
// kept: every row the prototype excludes is a row this harness counts as a MISS
// it cannot possibly hit, so this instrument can only UNDERSTATE recall relative
// to the frozen one. A denominator that silently matched by adopting the filter
// would flatter the score. Measured at the merged tree the gap is 8 rows, and
// the NUMERATOR reconciles exactly — both instruments hit the same 4,625
// witnessed pairs — which is what identifies the divergence as denominator-only.
//
// unindexed is that gap, counted every run: it is the CARDINALITY CONTROL for
// this denominator. Without it "total" is a bare number that a translation
// dropping most of the dump would shrink invisibly, and the divergence from the
// frozen instrument would be a fact nobody could see from the output.
func implVTARecall(
	sites []implFireIfaceSite, derived map[pair]bool, ifaceNodes map[string]string,
) (hit, total, unindexed int) {
	indexedIface := make(map[string]bool, len(ifaceNodes))
	for _, key := range ifaceNodes {
		indexedIface[key] = true
	}
	for _, s := range sites {
		for _, o := range s.oracle {
			tk := implReceiverKeyOf(o)
			if tk == "" {
				continue // a free function has no receiver type to implement with
			}
			total++
			if !indexedIface[s.ifaceKey] {
				// Counted in total ABOVE, deliberately: this row is in this
				// harness's denominator and out of the prototype's.
				unindexed++
			}
			if derived[pair{from: s.ifaceKey, to: tk}] {
				hit++
			}
		}
	}
	return hit, total, unindexed
}

// implReceiverKeyOf turns an oracle node ID "<file>:<Type>.<Method>" into the
// receiver's "<relDir>\x00<Type>" key, or "" when it names no method.
func implReceiverKeyOf(nodeID string) string {
	nodeID = strings.TrimSpace(nodeID)
	i := strings.LastIndex(nodeID, ":")
	if i <= 0 {
		return ""
	}
	file, symbol := nodeID[:i], nodeID[i+1:]
	j := strings.LastIndex(symbol, ".")
	if j <= 0 {
		return ""
	}
	return filepath.ToSlash(filepath.Dir(filepath.ToSlash(file))) + implOracleSep + symbol[:j]
}

// implTargetingHitRate measures interface-decl CALLS targeting, AT PAIR LEVEL.
//
// THE DENOMINATOR IS THE PLAN'S, STATED IN FULL because a hit rate without its
// denominator is not a measurement: "the sites whose qualifier type resolves to
// an in-repo interface". That population is not something the code graph
// publishes — deciding it needs the qualifier's TYPE — so it is taken from the
// frozen VTA site dump, which decided exactly that question with a real type
// analysis and is one of this work's frozen instruments. A site counts toward
// the denominator when VTA classified it as an interface dispatch AND the
// interface it named is one this collector actually indexed; an interface
// outside the indexed universe is not a site this work could have targeted.
//
// THE NUMERATOR is "the sites whose resolved target is an interface method
// node": the site's own caller declaration has a CALLS edge to
// `<file>:<Interface>.<Method>` for that interface and that method name.
//
// PAIR LEVEL, not edge level: one (caller, name) group is ONE site however many
// candidate edges it produced, so an unbound site fanning out to twenty
// candidates cannot outweigh twenty bound ones.
func implTargetingHitRate(
	edges []*knowledgev1.Edge, ifaceNodes map[string]string, sites []implFireIfaceSite,
) (hits, denom int) {
	// The interface node ID each site's oracle key names.
	nodeIDByKey := make(map[string]string, len(ifaceNodes))
	for id, key := range ifaceNodes {
		nodeIDByKey[key] = id
	}
	callEdge := make(map[string]bool, len(edges))
	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) == kgtypes.EdgeCalls {
			callEdge[e.FromId+"->"+e.ToId] = true
		}
	}
	for _, s := range sites {
		ifaceID, indexed := nodeIDByKey[s.ifaceKey]
		if !indexed {
			continue // an interface outside the indexed universe
		}
		denom++
		if callEdge[s.caller+"->"+ifaceID+"."+s.name] {
			hits++
		}
	}
	return hits, denom
}

// implOneMethodCensus reports the single-method interface population, which the
// ticket requires be visible rather than suppressed: structurally identical
// one-method interfaces legitimately share satisfiers, and that is correct Go.
func implOneMethodCensus(edges []*knowledgev1.Edge, typeNodes map[string]string) (ifaces, pairs int) {
	seen := map[string]bool{}
	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeImplements {
			continue
		}
		if _, isType := typeNodes[e.FromId]; !isType {
			continue
		}
		if e.Method != kgtypes.EdgeMethodMethodSet+"1" {
			continue
		}
		pairs++
		if !seen[e.FromId] {
			seen[e.FromId] = true
			ifaces++
		}
	}
	return ifaces, pairs
}
