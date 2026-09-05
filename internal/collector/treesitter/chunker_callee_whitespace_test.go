// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// calleeWhitespace is every whitespace rune a composed callee span can pick up
// from source layout. A callee name contains none of these in any grammar the
// chunker parses, so any one of them in an emitted callee is layout that leaked
// into an index key.
const calleeWhitespace = " \t\n\r\v\f"

// plainDottedName matches a callee that is a bare or separator-qualified name —
// the shape that can bind to a declaration. Everything else is structurally
// unbindable residue, which this census LOGS and never asserts on. The
// separators cover the spellings the chunker emits verbatim from source: `.`
// (most languages), `::` (Rust, C++, PHP static), `:` (Lua methods) and `->`
// (PHP instance).
//
// Package-level so the walk below compiles it once rather than per callee.
var plainDottedName = regexp.MustCompile(`^[\p{L}_$@][\p{L}\p{N}_$]*(([.:]{1,2}|->)[\p{L}_$][\p{L}\p{N}_$]*)*$`)

// censusSkipDirs are the directory names the corpus walk refuses to descend
// into.
//
// THE .claude EXCLUSION IS LOAD-BEARING, not cosmetic: .claude/worktrees holds
// full checkouts of this repo, so including it walks every defect site once per
// live worktree. Measured at whole-tree scope, excluding .claude walks 139,891
// edges in about 7s while including it walks 959,286 in 46s — a sevenfold
// inflation that also double-counts every site.
var censusSkipDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true,
	"build": true, "bin": true, ".claude": true,
}

// TestCalleeNamesCarryNoWhitespace is the standing corpus census guarding the
// composed callee span: it used to carry source whitespace INTO the emitted
// name, so a multi-line fluent chain or a multi-line qualified name bound to
// nothing.
//
// The assertion is an ABSOLUTE POST-FIX INVARIANT — zero emitted callees carry
// a whitespace rune — rather than a before-and-after identity. That matters
// because the chunker analyzes the repo it ships in: a before/after count would
// compare two different corpora. An absolute invariant holds for a correct
// implementation over ANY corpus, which is also why it doubles as this ticket's
// measured-acceptance instrument over a second, foreign corpus.
//
// IT NOW ASSERTS A SECOND, WIDER INVARIANT, and the widening is the point: a
// whitespace census goes to zero while a composite-literal receiver stays just
// as unbindable, because stripping whitespace made it whitespace-FREE and the
// instrument stopped seeing it. THE INSTRUMENT MUST MEASURE THE PROPERTY, NOT
// THE ARTIFACT. The wider invariant is "every emitted callee is a NAMEABLE
// callee", asserted at zero.
//
// ITS SCOPE IS THE DECLINE LANGUAGES, and stating that where the rule is
// declared matters: production declines exactly the languages whose callee
// profile sets DeclineNonName, so those are the ones this asserts over. For
// every other language — a shell today, and any language nobody has derived —
// the tally is LOGGED and never asserted, because a shell command word is not a
// name and this layer cannot tell a mangled one from a real one. That is a
// genuine limitation of the layer rather than a deferral: no input the chunker
// can obtain would resolve it.
//
// IT IS STRUCTURALLY BLIND TO THE BARE-NAME PRODUCERS, and the reason differs on
// each side of the fix. BEFORE, the bare names ARE emitted and they ARE
// nameable, so the predicate accepts them and neither tally counts them. AFTER,
// they are not emitted at all, so there is nothing left to count. Either way
// this census reads identically whether those declines fired or regressed —
// that is what makes it blind, not any coincidence of numbers. Do NOT widen the
// predicate to chase them: the census sees only the emitted STRING, and nothing
// in a bare `size` distinguishes a fabricated emission from a legitimate
// unqualified call. Their catchers are the per-language fixtures and the
// resolution-layer controls in the parser package; there is no corpus-level
// gate for that class and this test does not claim one.
//
// The four-bucket plainDottedName residue tally stays exactly as it was, still
// LOGGED and never asserted, so the comparison against the previous census
// stays readable.
//
// Roots: KNOWLEDGE_CALLEE_CENSUS_ROOT names a single directory to walk when
// set — that is how a foreign corpus is measured — and otherwise THIS MODULE's
// tree, resolved by repoRoot: cmd/knowledge in this repository, the mirror root
// in the published one, so the same call names this module's whole source in
// both layouts.
//
// THE SERVER MODULE IS THE OTHER HALF'S CORPUS. The invariant is
// corpus-independent — every emitted callee is nameable, whatever tree it came
// from — so splitting the corpus by module preserves the assertion exactly and
// the two halves together walk what the single test walked.
// chunker_callee_whitespace_server_test.go carries that half and the sync script
// removes it from the published tree, which has no server module to walk.
func TestCalleeNamesCarryNoWhitespace(t *testing.T) {
	var roots []string
	// FLOORS ARE MEASURED-THEN-ROUNDED-DOWN LITERALS, never tree-derived counts,
	// so ordinary drift cannot false-fail them. Measured at this tree:
	// cmd/knowledge walks 118,888 call edges and the staged mirror layout 119,726,
	// so 50,000 sits well below both.
	floor := 50000
	envRoot := os.Getenv("KNOWLEDGE_CALLEE_CENSUS_ROOT")
	if envRoot != "" {
		roots = []string{envRoot}
		floor = 1000
	} else {
		roots = []string{repoRoot(t)}
	}
	calleeCensusOverRoots(t, roots, floor)
}

// calleeCensusOverRoots runs the census over the roots given and asserts the two
// invariants at zero. It is the ONE body both halves of the split share, so the
// server half cannot drift into asserting something different from this one.
func calleeCensusOverRoots(t *testing.T, roots []string, floor int) {
	t.Helper()

	chunker := NewChunker()
	defer chunker.Close()

	total := 0
	dirty := 0
	var offenders []string

	// Residue buckets, LOGGED and never asserted. Classification is by the
	// POST-FIX predicate — what is still unbindable after this fix — not by the
	// pre-fix mechanism that mangled it.
	var brace, quote, paren, other int

	// The widened tally. unbindableDecline is ASSERTED at zero;
	// unbindableLogged is the same measurement over the languages production
	// does not decline for, and is reported only.
	unbindableDecline, unbindableLogged := 0, 0
	var unnameable []string

	for _, root := range roots {
		//nolint:gosec // walks a source tree the operator named: either this repo's own, or the corpus root supplied to measure a second corpus
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if censusSkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			// The language is kept rather than discarded: the widened tally
			// splits on it, because the assertion's scope is exactly the set of
			// languages production declines for.
			lang := DetectLanguage(path)
			if lang == LangUnknown {
				return nil
			}
			prof := calleeProfileFor(lang)
			src, readErr := os.ReadFile(path) //nolint:gosec // walks a source tree, same as the sibling census at chunker_test_calls_census_test.go
			if readErr != nil {
				return readErr
			}
			result, chunkErr := chunker.ChunkFile(context.Background(), path, src)
			if chunkErr != nil {
				return chunkErr
			}
			for _, e := range result.Edges {
				if e.Type != EdgeCalls && e.Type != EdgeTestCalls {
					continue
				}
				total++
				if strings.ContainsAny(e.ToID, calleeWhitespace) {
					dirty++
					if len(offenders) < 10 {
						// QUOTED deliberately: an offending callee's whole point
						// is that it holds a newline or a tab, which an unquoted
						// report would render as actual layout and hide.
						offenders = append(offenders,
							path+": "+e.FromID+" -> "+strconv.Quote(e.ToID))
					}
				}
				if !calleeIsNameable(e.ToID, prof.NameExtra) {
					if prof.DeclineNonName {
						unbindableDecline++
						if len(unnameable) < 10 {
							unnameable = append(unnameable,
								path+": "+e.FromID+" -> "+strconv.Quote(e.ToID))
						}
					} else {
						unbindableLogged++
					}
				}
				if plainDottedName.MatchString(e.ToID) {
					continue
				}
				switch {
				case strings.ContainsAny(e.ToID, "{}"):
					brace++
				case strings.ContainsAny(e.ToID, "\"`'"):
					quote++
				case strings.ContainsAny(e.ToID, "()[]"):
					paren++
				default:
					other++
				}
			}
			return nil
		})
		require.NoError(t, walkErr, "walking %s", root)
	}

	// KNOWN-POSITIVE CONTROL, without which a walk that found nothing would
	// pass as a clean census. The floor is chosen by each caller, well below its
	// own measured population, and is a literal rather than a tree-derived count.
	require.GreaterOrEqualf(t, total, floor,
		"the census walked %d call edges over %v, below the known-positive floor — it measured nothing",
		total, roots)

	t.Logf("callee census: total=%d dirty=%d roots=%v", total, dirty, roots)
	t.Logf("structurally-unbindable residue (LOGGED, NOT ASSERTED): brace=%d quote=%d paren=%d other=%d total=%d",
		brace, quote, paren, other, brace+quote+paren+other)
	// ONE UNBROKEN LINE. gofmt does not rewrap string literals, but a
	// hand-wrapped format string would make every gate grepping this token match
	// nothing while the file still looked correct.
	t.Logf("unbindable_in_decline_languages=%d unbindable_in_logged_languages=%d", unbindableDecline, unbindableLogged)

	require.Zerof(t, dirty,
		"%d of %d emitted callees carry a whitespace rune; first offenders:\n%s",
		dirty, total, strings.Join(offenders, "\n"))

	require.Zerof(t, unbindableDecline,
		"%d of %d emitted callees are not nameable in a language production declines for; first offenders:\n%s",
		unbindableDecline, total, strings.Join(unnameable, "\n"))
}
