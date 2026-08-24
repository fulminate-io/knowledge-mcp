// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// pair is one (from, to) edge endpoint pair, directly comparable across the
// baseline artifacts and a live run because both sides address an endpoint by
// the collector's own node ID: the artifacts record the IDs a collect emitted,
// and the live side reads FromId/ToId off the edges a collect just produced.
type pair struct{ from, to string }

// groupKey is the plan's GROUP: a caller together with the BARE NAME of the
// target. Every definition this test uses — the oracle construction, bareName,
// GROUP, FAN-OUT GROUP, FIRED, WRONG TARGET, DISAGREE-EMPTY-ORACLE, COVERAGE
// and the floors — is declared in the plan's Phase 1 Step 1 vocabulary block
// and is CITED here rather than restated in a form that could drift from it.
type groupKey struct {
	caller   string
	bareName string
}

// r2tCorpusExpect carries the per-corpus locked controls and the acceptance
// floor. The oracle cardinality is a KNOWN-POSITIVE CONTROL on this file's own
// reimplementation of the analyzer: a construction that silently diverges is
// otherwise invisible, and every downstream number would then be scored against
// the wrong oracle.
type r2tCorpusExpect struct {
	oracleSize int
	floorPct   float64
}

var r2tExpect = map[string]r2tCorpusExpect{
	// Floors are the BIND-ONLY coverage column (50.5 / 52.4) minus the ticket's
	// 5-point tolerance. They are NOT the full-heuristic column (54.6 / 65.7)
	// minus five: that column counts suppression groups which a bind-only rung
	// leaves at their existing target set, so floors derived from it would be
	// unreachable by any correct implementation of this ticket.
	"knowledge": {oracleSize: 51083, floorPct: 45.5},
	"agent":     {oracleSize: 40615, floorPct: 47.4},
}

// TestR2TParityScore is the ticket's ACCEPTANCE MEASUREMENT — it answers "did
// the rung meet the coverage floors with zero wrong targets", once, against the
// frozen corpora. It is NOT a standing regression gate: it is a whole-system
// aggregate, so an individual guard could regress and be absorbed by it without
// moving a floor. The standing gates are the rung's own subtest criteria.
//
//	R2T_ROOT=$HOME/.knowledge/tsparity go test ./internal/collector/codesync/ \
//	  -run '^TestR2TParityScore$' -v -count=1 -timeout 3600s
func TestR2TParityScore(t *testing.T) {
	root := os.Getenv("R2T_ROOT")
	if root == "" {
		t.Skip("set R2T_ROOT=<tsparity root> to run the R2T parity scoring")
	}
	// An operator-supplied path that flows into this test's own filesystem
	// reads and writes, so normalize it before handing it on.
	root = filepath.Clean(root)

	// SERIALLY, no t.Parallel: each subtest is a whole-repo Populate whose cost
	// is dominated by memory, and two concurrent runs would contend for it with
	// no wall-clock win.
	for _, label := range []string{"knowledge", "agent"} {
		t.Run(label, func(t *testing.T) {
			scoreR2TCorpus(t, root, label)
		})
	}
}

func scoreR2TCorpus(t *testing.T, root, label string) {
	t.Helper()
	expect, ok := r2tExpect[label]
	require.True(t, ok, "no locked controls for corpus %q", label)

	baseDir := filepath.Join(root, "baseline", label)
	corpusDir := filepath.Join(root, "corpora", label)
	postDir := filepath.Join(root, "post", label)

	// 1. The baseline artifacts: the PRE picture, frozen before the rung.
	prePairs := readPairTSV(t, filepath.Join(baseDir, "A_ts_dropped.tsv"))
	bPrecise := readPairTSV(t, filepath.Join(baseDir, "B_precise_added.tsv"))
	nodeIDs := readNodeIDs(t, filepath.Join(baseDir, "nodes.tsv"))
	covered := readLineSet(t, filepath.Join(baseDir, "covered_files.txt"))
	require.NotEmpty(t, prePairs, "control: the baseline PRE set is non-empty")

	// 2. The closure-corrected oracle B*, plus its cardinality control.
	oracle := buildR2TOracle(t, bPrecise, filepath.Join(baseDir, "precise_unbound.tsv"), nodeIDs)
	require.Lenf(t, oracle, expect.oracleSize,
		"want: the locked oracle cardinality for %s\ngot:  %d against locked %d — a divergent reconstruction, and every downstream number would be scored against the wrong oracle",
		label, len(oracle), expect.oracleSize)

	// 3. The POST picture from the CURRENT tree: every CALLS edge whose caller
	// is a Go declaration in a file the frozen baseline recorded as covered.
	pop, err := parser.Populate(t.Context(), filepath.Base(corpusDir), corpusDir)
	require.NoError(t, err)
	idx := buildNodeIndex(pop.Nodes)
	// Pre-sized from the PRE set, which is the same order of magnitude.
	postPairs := make(map[pair]bool, len(prePairs))
	for _, e := range pop.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls {
			continue
		}
		if idx.langByID[e.FromId] != string(treesitter.LangGo) {
			continue
		}
		if !covered[idx.fileByID[e.FromId]] {
			continue
		}
		postPairs[pair{from: e.FromId, to: e.ToId}] = true
	}
	require.NotEmpty(t, postPairs, "control: the POST set is non-empty")

	// 4. Group all three sets, and take the fan-out population from PRE.
	preByGroup := groupPairs(prePairs)
	postByGroup := groupPairs(postPairs)
	oracleByGroup := groupPairs(oracle)

	var fanout, fired, exact, subset, partial, disagreeEmpty, wrongTargets int
	var wrongDetail, firedDetail []string
	for g, preSet := range preByGroup {
		if len(preSet) < 2 {
			continue
		}
		fanout++
		postSet := postByGroup[g]
		if setsEqual(preSet, postSet) {
			continue
		}
		fired++

		oracleSet := oracleByGroup[g]
		firedDetail = append(firedDetail, fmt.Sprintf("%s\t%s\tpre=%s\tpost=%s\toracle=%s",
			g.caller, g.bareName, joinSet(preSet), joinSet(postSet), joinSet(oracleSet)))

		if len(oracleSet) == 0 {
			// DISAGREE-EMPTY-ORACLE: the rung bound and PRECISION EMITS
			// NOTHING AT ALL for this group. Reported, never gated — gating it
			// at zero would be unsatisfiable by correct work.
			disagreeEmpty++
			continue
		}
		// THE SWITCH IS TOTAL, AND THAT IS THE POINT. Its first form had no
		// default and no partial bucket, so a group whose POST overlapped the
		// oracle AND carried extra targets fell through every case and was
		// counted NOWHERE — 16 groups on knowledge. That residue is exactly
		// where a partial mis-binding hides: {A,B,C} narrowed to {A,B} against
		// an oracle of {A} is neither exact, nor a subset, nor disjoint, so an
		// untotalled switch reports it as nothing at all while the coverage and
		// wrong-target numbers both stay clean.
		//
		// subset + partial reconstructs the analyzer's own agreeSub category
		// (17 on knowledge), so the decomposition still cross-checks against
		// report.go while keeping the residue visible instead of folded away.
		switch {
		case setsEqual(postSet, oracleSet):
			exact++
		case subsetOf(postSet, oracleSet):
			subset++
		case intersects(postSet, oracleSet):
			// PARTIAL: overlaps the oracle but carries targets it does not name.
			partial++
		default:
			// WRONG TARGET: fired, oracle NON-EMPTY, and no overlap at all.
			wrongTargets++
			wrongDetail = append(wrongDetail, fmt.Sprintf("%s\t%s\tpost=%s\toracle=%s",
				g.caller, g.bareName, joinSet(postSet), joinSet(oracleSet)))
		}
	}

	// BIND-ONLY EMITS NO SUPPRESSION EVENTS. The rung either positively binds a
	// group or leaves it exactly as it was, so the analyzer's two suppression
	// buckets are structurally zero here. The fields are kept so this line
	// stays readable against the analyzer's own vocabulary.
	suppressOK, suppressBad := 0, 0

	var coverage float64
	if fanout > 0 {
		coverage = 100 * float64(fired) / float64(fanout)
	}

	preHits := countHits(prePairs, oracle)
	postHits := countHits(postPairs, oracle)

	// 5. THE LOCKED LINE. It must START the line — the acceptance criterion
	// anchors on `^R2TSCORE ` so a diagnostic quoting the text cannot be
	// counted — and wrong_targets must not be last, so it always carries a
	// trailing space for the criterion's `wrong_targets=0 ` match. Diagnostics
	// in this file use want:/got: and NEVER this prefix.
	fmt.Printf("R2TSCORE corpus=%s fanout_groups=%d fired=%d coverage_pct=%.1f exact=%d subset=%d "+
		"disagree_empty_oracle=%d wrong_targets=%d suppress_ok=%d suppress_bad=%d "+
		"pre_pairs=%d post_pairs=%d precision_pre=%.1f precision_post=%.1f recall_pre=%.1f recall_post=%.1f "+
		"partial=%d\n",
		label, fanout, fired, coverage, exact, subset,
		disagreeEmpty, wrongTargets, suppressOK, suppressBad,
		len(prePairs), len(postPairs),
		pct(preHits, len(prePairs)), pct(postHits, len(postPairs)),
		pct(preHits, len(oracle)), pct(postHits, len(oracle)),
		partial)

	// THE PARTITION ASSERTION, so a residue bucket cannot reappear silently.
	// Every FIRED group lands in exactly one class; if a future edit adds a
	// case without adding it here, this fails rather than under-reporting.
	require.Equalf(t, fired, exact+subset+partial+disagreeEmpty+wrongTargets,
		"want: the classification to PARTITION the fired groups for %s\ngot:  a residue counted in no bucket", label)

	// 6. Per-group detail, so a wrong target can be READ rather than inferred.
	writeDetail(t, postDir, "fired_groups.tsv", firedDetail)
	writeDetail(t, postDir, "wrong_targets.tsv", wrongDetail)

	// 7. VACUITY CONTROL, BEFORE the wrong-target assertion: a zero wrong-target
	// count is also exactly what a harness that never fires produces.
	require.Positivef(t, fanout, "want: a non-empty fan-out population for %s\ngot:  zero fan-out groups — the scoring would be vacuous", label)
	require.Positivef(t, fired, "want: the rung to have fired somewhere on %s\ngot:  zero fired groups — the scoring would be vacuous", label)

	// 8. THE TICKET'S T1-CLASS GATE.
	require.Zerof(t, wrongTargets,
		"want: zero wrong targets on %s\ngot:  %d — see %s", label, wrongTargets,
		filepath.Join(postDir, "wrong_targets.tsv"))

	// 9. THE BIND-ONLY COVERAGE FLOOR.
	require.GreaterOrEqualf(t, coverage, expect.floorPct,
		"want: coverage at or above the bind-only floor for %s\ngot:  %.1f against %.1f", label, coverage, expect.floorPct)
}

// buildR2TOracle reconstructs B*, the closure-corrected precise oracle, exactly
// as the preserved analyzer constructs it.
func buildR2TOracle(t *testing.T, bPrecise map[pair]bool, unboundPath string, nodeIDs map[string]bool) map[pair]bool {
	t.Helper()
	oracle := make(map[pair]bool, len(bPrecise)*2)
	for p := range bPrecise {
		oracle[p] = true
	}
	for _, cols := range readTSVRows(t, unboundPath) {
		if len(cols) < 3 {
			continue
		}
		callerKey, calleeKey := cols[1], cols[2]
		// CALLEE-SIDE ROOTING MANUFACTURES REVERSED EDGES; skipping it was the
		// correction that produced the locked figures.
		if strings.Contains(calleeKey, "$") {
			continue
		}
		from := callerKey
		if i := strings.Index(from, "$"); i >= 0 {
			from = from[:i]
		}
		to := calleeKey
		if from == to || !nodeIDs[from] || !nodeIDs[to] {
			continue
		}
		oracle[pair{from: from, to: to}] = true
	}
	return oracle
}

// bareR2TName is the plan's bareName: the text after the LAST ':', then after
// the LAST '.' of that.
func bareR2TName(nodeID string) string {
	s := nodeID
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func groupPairs(pairs map[pair]bool) map[groupKey]map[string]bool {
	out := make(map[groupKey]map[string]bool, len(pairs))
	for p := range pairs {
		g := groupKey{caller: p.from, bareName: bareR2TName(p.to)}
		if out[g] == nil {
			out[g] = map[string]bool{}
		}
		out[g][p.to] = true
	}
	return out
}

func setsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func subsetOf(a, b map[string]bool) bool {
	if len(a) == 0 {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func intersects(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

func countHits(pairs, oracle map[pair]bool) int {
	n := 0
	for p := range pairs {
		if oracle[p] {
			n++
		}
	}
	return n
}

func pct(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return 100 * float64(num) / float64(den)
}

func joinSet(s map[string]bool) string {
	if len(s) == 0 {
		return "-"
	}
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func readPairTSV(t *testing.T, path string) map[pair]bool {
	t.Helper()
	out := map[pair]bool{}
	for _, cols := range readTSVRows(t, path) {
		if len(cols) < 2 {
			continue
		}
		out[pair{from: cols[0], to: cols[1]}] = true
	}
	return out
}

func readNodeIDs(t *testing.T, path string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, cols := range readTSVRows(t, path) {
		if len(cols) > 0 && cols[0] != "" {
			out[cols[0]] = true
		}
	}
	require.NotEmpty(t, out, "control: the node dump is non-empty")
	return out
}

func readLineSet(t *testing.T, path string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, line := range readLines(t, path) {
		if line != "" {
			out[line] = true
		}
	}
	require.NotEmpty(t, out, "control: the covered-file set is non-empty")
	return out
}

func readTSVRows(t *testing.T, path string) [][]string {
	t.Helper()
	lines := readLines(t, path)
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "\t"))
	}
	return rows
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	//nolint:gosec // G304: an operator-supplied artifact path on a test-only,
	// env-gated instrument — process-owned rather than request-derived, and
	// Cleaned at the top of the test.
	f, err := os.Open(path)
	require.NoErrorf(t, err, "reading baseline artifact %s", path)
	defer func() { require.NoError(t, f.Close()) }()

	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	require.NoError(t, sc.Err())
	return out
}

func writeDetail(t *testing.T, dir, name string, lines []string) {
	t.Helper()
	//nolint:gosec // G301: operator-supplied output directory on a test-only,
	// env-gated instrument, per the note on readLines.
	require.NoError(t, os.MkdirAll(dir, 0o750))
	sort.Strings(lines)
	// AN EMPTY LIST WRITES AN EMPTY FILE, not a lone newline. wrong_targets.tsv
	// is normally empty, and a file holding "\n" reads as one blank record to
	// anything that counts lines — which is the same shape a genuinely missing
	// wrong target would take.
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	//nolint:gosec // G703: same class — the path is process-owned.
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
}
