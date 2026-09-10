// SPDX-License-Identifier: Apache-2.0

package tools

// query_arm_count_comment_census_test.go pins every ARM-COUNT CLAIM WRITTEN IN
// PROSE in query_arm_registry.go to the declarations those claims are about.
//
// WHY A TEST AND NOT A REVIEW DUTY. queryArmCount is a locked literal and the
// bijection test holds the registry to it, so an arm that lands without updating
// the constant fails loudly. The FILE HEADER's own count paragraph had no such
// hold: adding armPracticeStyleIndex moved the constant from 50 to 51 and left
// the header reading "ARM COUNT: 50", and the per-entry-point sentence beside it
// had been reading 9 against a block of 12 since before that. Both are claims a
// reader trusts and neither had an instrument.
//
// WHAT IT PINS, all derived from the SOURCE rather than restated here:
//   - the number of armID constants declared, against queryArmCount;
//   - every "<EntryPoint> — N arms (locked floor F)" group comment, against the
//     number of constants declared under it;
//   - every "ARM COUNT: N", "The N query dispatch arms" and "each of the N arms"
//     prose claim, against queryArmCount;
//   - every "<EntryPoint> takes N against a floor of F" prose claim, against
//     that entry point's group comment and its constant count.
//
// SO THE NEXT ARM CANNOT DRIFT THE COMMENT SILENTLY: an arm added under a group
// whose header keeps its old number turns this red, naming the group.
//
// IT READS COMMENT TEXT, which is why it parses with parser.ParseComments rather
// than reusing parseToolsPackage — that walker drops comments on purpose, since
// the claim-surface census is about declarations. An ast pattern cannot match a
// comment node at all, so source text is the only instrument for this class.
//
// PERF SHAPE: one go/parser pass over one file at test time.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// queryArmRegistrySource is the file whose prose this census holds to its own
// declarations.
const queryArmRegistrySource = "query_arm_registry.go"

// The five claim shapes, as they are written in the file.
var (
	// armGroupHeaderRe matches a const-block group comment:
	//   "InterceptQueryPracticeLinkage — 12 arms (locked floor 8)."
	armGroupHeaderRe = regexp.MustCompile(`(\w+) — (\d+) arms? \(locked floor (\d+)\)`)
	// armProseCountRe matches the file header's own total:
	//   "ARM COUNT: 51, against the plan's seven locked ..."
	armProseCountRe = regexp.MustCompile(`ARM COUNT: (\d+)`)
	// armProseDispatchRe matches the const block's lead sentence:
	//   "The 51 query dispatch arms."
	armProseDispatchRe = regexp.MustCompile(`The (\d+) query dispatch arms`)
	// armProseEachOfRe matches the schema paragraph's own total:
	//   "a cell on each of the 51 arms"
	//
	// It is pinned for a reason worth recording: this sentence read 51 while the
	// constant was 50, so it went RIGHT BY ACCIDENT when the 51st arm landed. A
	// prose count that is accidentally true is not maintained — it is the same
	// unpinned claim, one edit away from being wrong again.
	armProseEachOfRe = regexp.MustCompile(`each of the (\d+) arms`)
	// armProseTakesRe matches the per-entry-point sentence in the header:
	//   "InterceptQueryPracticeLinkage takes 12 against a floor of 8"
	armProseTakesRe = regexp.MustCompile(`(\w+) takes (\d+) against a floor of (\d+)`)
)

// armGroupCensus is what one group comment claims and what the source declares
// under it.
type armGroupCensus struct {
	entryPoint string
	claimed    int
	floor      int
	declared   int
	line       int
}

// censusQueryArmGroups parses query_arm_registry.go and derives, per group
// comment, the number of armID constants declared beneath it.
//
// A SECTION IS DELIMITED BY THE BLANK LINE, which is how the const block is
// actually written, and it is delimited that way rather than "runs to the next
// header" for a reason a first attempt at this census got wrong: three of the
// block's sections carry a heading that states no count ("Single-arm per-graph
// entry points."), so a group that ran to the next COUNTED header swallowed them
// and reported 11 arms under a group of 2. A section whose heading states a
// count is censused; a section whose heading does not is skipped, and its
// constants belong to neither.
//
// A CONSTANT CARRYING ITS OWN DOC COMMENT WITH NO BLANK LINE BEFORE IT stays in
// its section — armBuiltinGraphStats is that shape today, and treating any doc
// comment as a section break would drop it out of the block's accounting.
func censusQueryArmGroups(t *testing.T) (groups []armGroupCensus, totalArmIDs int, comments []string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, queryArmRegistrySource, nil, parser.ParseComments)
	require.NoErrorf(t, err, "parsing %s", queryArmRegistrySource)

	for _, cg := range file.Comments {
		comments = append(comments, cg.Text())
	}

	current, prevEnd := -1, 0
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, isValue := spec.(*ast.ValueSpec)
			if !isValue {
				continue
			}
			if ident, isIdent := vs.Type.(*ast.Ident); !isIdent || ident.Name != "armID" {
				continue
			}
			totalArmIDs += len(vs.Names)

			start := vs.Pos()
			if vs.Doc != nil {
				start = vs.Doc.Pos()
			}
			startLine := fset.Position(start).Line
			if startLine-prevEnd > 1 { // a blank line: a new section opens here.
				current = -1
				if vs.Doc != nil {
					if m := armGroupHeaderRe.FindStringSubmatch(vs.Doc.Text()); m != nil {
						groups = append(groups, armGroupCensus{
							entryPoint: m[1],
							claimed:    mustAtoi(t, m[2]),
							floor:      mustAtoi(t, m[3]),
							line:       fset.Position(vs.Pos()).Line,
						})
						current = len(groups) - 1
					}
				}
			}
			prevEnd = fset.Position(vs.End()).Line
			if current >= 0 {
				groups[current].declared += len(vs.Names)
			}
		}
	}
	return groups, totalArmIDs, comments
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	require.NoErrorf(t, err, "parsing %q as a count", s)
	return n
}

// TestQueryArmCount_ProseClaimsMatchTheDeclarations is the census.
func TestQueryArmCount_ProseClaimsMatchTheDeclarations(t *testing.T) {
	groups, totalArmIDs, comments := censusQueryArmGroups(t)

	// VACUITY GUARDS FIRST. Each assertion below is an equality over a set the
	// walk derived; a walk that derived nothing would satisfy every one of them.
	require.NotEmpty(t, groups, "the census must find group comments — an empty walk proves nothing")
	require.NotZero(t, totalArmIDs, "the census must find armID constants")
	require.NotEmpty(t, comments, "the census must find comments — it is reading prose")

	t.Run("the declared armIDs are queryArmCount", func(t *testing.T) {
		assert.Equal(t, queryArmCount, totalArmIDs,
			"queryArmCount is a locked literal and %s declares %d armID constants",
			queryArmRegistrySource, totalArmIDs)
	})

	byEntryPoint := map[string]armGroupCensus{}
	t.Run("every group comment counts its own constants", func(t *testing.T) {
		for _, g := range groups {
			byEntryPoint[g.entryPoint] = g
			assert.Equalf(t, g.declared, g.claimed,
				"%s:%d — the group comment says %s takes %d arms and %d armID constants are declared under it; "+
					"an arm added to a group whose header keeps its old number is exactly the drift this census exists to catch",
				queryArmRegistrySource, g.line, g.entryPoint, g.claimed, g.declared)
			assert.GreaterOrEqualf(t, g.declared, g.floor,
				"%s declares %d arms against its locked floor of %d — a floor is a minimum",
				g.entryPoint, g.declared, g.floor)
		}
	})

	joined := strings.Join(comments, "\n")

	t.Run("every prose total is queryArmCount", func(t *testing.T) {
		claims := proseCounts(t, joined, armProseCountRe, "ARM COUNT: N")
		claims = append(claims, proseCounts(t, joined, armProseDispatchRe, "The N query dispatch arms")...)
		claims = append(claims, proseCounts(t, joined, armProseEachOfRe, "each of the N arms")...)
		require.NotEmpty(t, claims, "the file must carry at least one prose arm-count claim")
		for _, c := range claims {
			assert.Equalf(t, queryArmCount, c.value,
				"%s in %s claims %d arms while queryArmCount is %d — a prose count with no instrument "+
					"is what left the header reading 50 after the constant moved to 51",
				c.shape, queryArmRegistrySource, c.value, queryArmCount)
		}
	})

	t.Run("every per-entry-point sentence matches its group", func(t *testing.T) {
		matches := armProseTakesRe.FindAllStringSubmatch(joined, -1)
		require.NotEmpty(t, matches,
			"the header paragraph must carry at least one \"X takes N against a floor of F\" sentence")
		for _, m := range matches {
			entry, takes, floor := m[1], mustAtoi(t, m[2]), mustAtoi(t, m[3])
			g, known := byEntryPoint[entry]
			require.Truef(t, known,
				"the header names %q, which no const-block group comment declares — "+
					"a sentence about an entry point the block does not group cannot be checked against anything", entry)
			assert.Equalf(t, g.declared, takes,
				"the header says %s takes %d arms; the const block declares %d under its group comment",
				entry, takes, g.declared)
			assert.Equalf(t, g.floor, floor,
				"the header gives %s a floor of %d; its group comment gives it %d", entry, floor, g.floor)
		}
	})
}

// proseClaim is one numeric claim found in the file's comments.
type proseClaim struct {
	shape string
	value int
}

func proseCounts(t *testing.T, text string, re *regexp.Regexp, shape string) []proseClaim {
	t.Helper()
	var out []proseClaim
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		out = append(out, proseClaim{shape: fmt.Sprintf("%q", shape), value: mustAtoi(t, m[1])})
	}
	return out
}
