// SPDX-License-Identifier: Apache-2.0

package bootstrap

// check_daemon_route_census_shapes_test.go — THE CENSUS IS TESTED THE WAY IT
// WILL BE BROKEN.
//
// A census is an instrument, and an instrument that has never been shown to fire
// is a decoration. Every row here is a refusal shape Go actually admits, spliced
// into a copy of check_daemon_route.go IN MEMORY and type-checked in this
// package through the same auditRefusalCensus the real test uses — the copy
// stands in for the file on disk through go/types, so no source is written
// anywhere. The shapes that beat the four earlier instruments are marked
// wasSilent, and each is asserted RED here by the words the census answers with.
// The shapes that beat the fourth instrument in particular — a typed error, a
// sentinel from a second site, an aliased fmt — are in
// check_daemon_route_census_shapes_types_test.go, which appends its rows to the
// table this file runs.
//
// THE CONTROL IS IN THE SAME RUN: the unmutated file through the same function
// with the real rows must produce NO complaints, so a red is the mutation and not
// a census that complains about everything.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// censusShapeCase is one refusal shape and the words the census must answer with.
type censusShapeCase struct {
	name string
	// wasSilent records whether an instrument this census replaces missed the
	// shape. It is documentation of why the row exists, not an input.
	wasSilent bool
	// mutate rewrites the source. It is nil for a case whose defect is in the
	// TABLE rather than in the code — a row lying about what the code says.
	mutate func(t *testing.T, src string) string
	rows   func(rows []refusalRow) []refusalRow
	want   string
}

func TestRefusalCensusSeesEveryRefusalShape(t *testing.T) {
	source, err := censusReadSource(routeSourceFile)
	require.NoError(t, err)
	tests := packageTestFuncs(t)

	// CONTROL, same run, same function, same rows: the file as it stands passes.
	control, arms, err := auditRefusalCensus(nil, refusalRows, tests)
	require.NoError(t, err)
	require.NotEmpty(t, arms, "control: the enumeration must find refusal sites at all")
	require.Empty(t, control, "control: the unmutated file must pass, or every red below is the census complaining about everything")
	t.Logf("control: %d refusal sites, 0 complaints", len(arms))

	for _, tc := range censusShapeCases() {
		t.Run(tc.name, func(t *testing.T) {
			mutated := string(source)
			if tc.mutate != nil {
				mutated = tc.mutate(t, string(source))
				require.NotEqual(t, string(source), mutated, "the mutation must change the source, or this row proves nothing")
			}

			rows := append([]refusalRow(nil), refusalRows...)
			if tc.rows != nil {
				rows = tc.rows(rows)
			}
			require.False(t, tc.mutate == nil && tc.rows == nil, "a case must mutate the source or the table, or it tests nothing")
			complaints, _, err := auditRefusalCensus([]byte(mutated), rows, tests)
			require.NoError(t, err, "the mutated copy must still parse and type-check, or the red is a broken splice rather than the census")
			require.NotEmpty(t, complaints, "the census must refuse this shape, and it said nothing")
			assert.Contains(t, strings.Join(complaints, "\n"), tc.want)
			t.Logf("silent on an earlier instrument: %v — census says: %s", tc.wasSilent, strings.Join(complaints, "\n"))
		})
	}
}

// undeclared is the words the census answers with for a site it found and no row
// declares, whatever shape that site took.
const undeclared = "hands back an error this census does not declare"

// censusShapeCases is the whole table: the shapes the earlier rounds established,
// then the ones the type-directed enumeration added.
func censusShapeCases() []censusShapeCase {
	cases := append(censusShippedShapeCases(), censusTypedShapeCases()...)
	return append(cases, censusResultShapeCases()...)
}

// censusShippedShapeCases are the thirteen shapes carried from the rounds before
// the type checker. Every one of them still reds.
func censusShippedShapeCases() []censusShapeCase {
	return []censusShapeCase{
		{
			name: "a refusal returned from a helper function", want: undeclared,
			mutate: func(_ *testing.T, src string) string {
				return src + `
func probeHelperRefusal(n int) error {
	if n == 7 {
		return fmt.Errorf("check run: probe: a helper refusal for %d", n)
	}
	return nil
}
`
			},
		},
		{
			name: "a new arm wrapping a cause with a verb", want: undeclared,
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\tctx, cancel := context.WithTimeout(context.Background(), checkDaemonScanBudget)",
					`	if len(body) == 7 {
		return "", fmt.Errorf("check run: probe: the %s request body is seven bytes: %w", tool, io.EOF)
	}

`)
			},
		},
		{
			name: "a refusal inside a func literal", wasSilent: true, want: undeclared,
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\t\t\tconn, err := d.DialContext(ctx, network, addr)",
					`			if addr == "impossible.invalid:1" {
				return nil, fmt.Errorf("check run: probe: the dialer refuses %s", addr)
			}
`)
			},
		},
		{
			name: "a refusal assigned to a named result and returned bare", wasSilent: true, want: undeclared,
			mutate: func(_ *testing.T, src string) string {
				return src + `
func probeNamedResult(n int) (err error) {
	if n == 7 {
		err = fmt.Errorf("check run: probe: a named-result refusal for %d", n)
		return
	}
	return nil
}
`
			},
		},
		{
			name: "a refusal returned directly from a switch case", wasSilent: true, want: undeclared,
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\traw, err := io.ReadAll(resp.Body)",
					`	switch resp.StatusCode {
	case 299:
		return "", fmt.Errorf("check run: probe: the %s call answered 299 so no verdict was produced", tool)
	}
`)
			},
		},
		{
			name: "a package-level errors.New sentinel", wasSilent: true, want: undeclared,
			mutate: func(t *testing.T, src string) string {
				src = spliceBefore(t, src, "\t\"context\"", "\t\"errors\"\n")
				src = spliceBefore(t, src, "const mcpSessionHeader",
					"var errProbeSentinel = errors.New(\"check run: probe: a package-level sentinel refusal\")\n\n")
				return spliceBefore(t, src, "\tsession := resp.Header.Get(mcpSessionHeader)",
					`	if resp.StatusCode == 299 {
		return "", errProbeSentinel
	}
`)
			},
		},
		{
			name: "a fmt.Errorf whose format is a const", wasSilent: true, want: "is not a static string literal",
			mutate: func(t *testing.T, src string) string {
				src = spliceBefore(t, src, "const checkDaemonToolCallID",
					"const probeConstFormat = \"check run: probe: a const-format refusal for %s\"\n\n")
				return spliceBefore(t, src, "\tencodedArgs, err := json.Marshal(args)",
					`	if tool == "impossible" {
		return "", fmt.Errorf(probeConstFormat, tool)
	}
`)
			},
		},
		{
			// The round-3 laundering: one dead marshal in front of a fully
			// drivable refusal, and a row calling that refusal unreachable.
			name: "a dead json.Marshal laundering a drivable refusal", wasSilent: true,
			want: "does not carry the error of a json.Marshal, so it is drivable, but its row calls it unreachable",
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\traw, err := io.ReadAll(resp.Body)",
					`	_, _ = json.Marshal(struct{}{})
	if resp.StatusCode == 299 {
		return "", fmt.Errorf("check run: probe: the daemon at %s answered 299, so no scan ran", endpoint)
	}
`)
			},
			rows: func(rows []refusalRow) []refusalRow {
				return append(rows, refusalRow{
					construction:       "check run: probe: the daemon at %s answered 299, so no scan ran",
					unreachableBecause: "guarded by a json.Marshal this file builds",
				})
			},
		},
		{
			// The census classifies errors.New and fmt.Errorf. Anything else out
			// of the errors package is refused rather than walked past, because a
			// construction it walks past is a site outside the class.
			name: "an error constructor the census does not classify", wasSilent: true,
			want: "an error constructor this census does not classify",
			mutate: func(t *testing.T, src string) string {
				src = spliceBefore(t, src, "\t\"context\"", "\t\"errors\"\n")
				return spliceBefore(t, src, "\tsession := resp.Header.Get(mcpSessionHeader)",
					`	if resp.StatusCode == 299 {
		return "", errors.Join(nil, nil)
	}
`)
			},
		},
		{
			// A row may not call a site unreachable on the strength of a marshal
			// of an IDENTIFIER without naming the test that says what is in it.
			name: "an unreachable row resting on a marshaled identifier with no provenance test",
			want: "Name the test that pins that in valueProvenanceBy",
			rows: func(rows []refusalRow) []refusalRow {
				return withoutProvenanceTest(rows)
			},
		},
		{
			// And the test it names must exist, on the same terms as observedBy.
			name: "an unreachable row naming a provenance test that was renamed away",
			want: `names valueProvenanceBy "TestRenamedAway", which is not declared in this package`,
			rows: func(rows []refusalRow) []refusalRow {
				for i := range rows {
					if rows[i].valueProvenanceBy != "" {
						rows[i].valueProvenanceBy = "TestRenamedAway"
					}
				}
				return rows
			},
		},
		{
			// The identity half of the guard: a marshal whose error is live but
			// which the refusal does not carry guards that refusal of nothing.
			name: "a live json.Marshal whose error the refusal does not carry", wasSilent: true,
			want: "does not carry the error of a json.Marshal, so it is drivable, but its row calls it unreachable",
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\traw, err := io.ReadAll(resp.Body)",
					`	_, probeErr := json.Marshal(struct{}{})
	if probeErr != nil {
		return "", fmt.Errorf("check run: probe: the daemon at %s answered oddly, so no scan ran", endpoint)
	}
`)
			},
			rows: func(rows []refusalRow) []refusalRow {
				return append(rows, refusalRow{
					construction:       "check run: probe: the daemon at %s answered oddly, so no scan ran",
					unreachableBecause: "guarded by a json.Marshal this file builds",
				})
			},
		},
		{
			// Two sites wearing one construction make the table unable to say
			// which is which, so the census refuses the collision rather than
			// picking.
			name: "two sites sharing one construction", wasSilent: true,
			want: "two refusal sites carry one construction",
			mutate: func(t *testing.T, src string) string {
				return spliceBefore(t, src, "\traw, err := io.ReadAll(resp.Body)",
					`	if resp.StatusCode == 299 {
		return "", fmt.Errorf(
			"check run: the knowledge daemon at %s answered the %s call with HTTP %d, so no verdict was produced",
			endpoint, tool, resp.StatusCode)
	}
`)
			},
		},
	}
}

// withoutProvenanceTest strips the provenance test name from every row that
// carries one, leaving the unreachability claim resting on nothing.
func withoutProvenanceTest(rows []refusalRow) []refusalRow {
	for i := range rows {
		rows[i].valueProvenanceBy = ""
	}
	return rows
}

// spliceBefore inserts text ahead of an anchor that must appear EXACTLY ONCE, so
// a mutation cannot silently land in the wrong function when the source moves.
func spliceBefore(t *testing.T, src, anchor, insert string) string {
	t.Helper()
	require.Equal(t, 1, strings.Count(src, anchor), "the anchor %q must appear exactly once in %s", anchor, routeSourceFile)
	return strings.Replace(src, anchor, insert+anchor, 1)
}
