// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/parser"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// sameFamilyCallPairs lists ordered (caller language, callee language) pairs that
// are the SAME project language wearing two chunker labels, so a CALLS edge
// between them is not a language-boundary crossing. A .tsx component calling a
// function declared in a .ts module is one call inside one TypeScript project.
//
// The labels themselves are correct and other consumers read them, so the
// exception is scoped to this assertion rather than collapsing the chunker's
// language vocabulary.
//
// It is consulted at the APPEND site below, ahead of the violation buffer's cap:
// filtering at the report site instead would let same-family pairs fill the
// five-slot buffer and hide a genuine crossing behind them.
var sameFamilyCallPairs = map[[2]string]bool{
	{"tsx", "typescript"}: true,
	{"typescript", "tsx"}: true,
}

// TestCallsLanguageCensus_EdgeProbe runs the full Populate path over a REAL
// polyglot repository and censuses the shipped CALLS edges by language.
//
// It is a permanent gate, not a one-off instrument: it is the only test that
// exercises CALLS derivation at corpus scale, it costs nothing when
// CALLS_CENSUS_REPO is unset, and every defect it guards was invisible to
// fixture-scale tests for as long as those defects existed.
//
// Point CALLS_CENSUS_REPO at any checkout carrying a go.mod plus at least one
// non-Go language:
//
//	CALLS_CENSUS_REPO=/path/to/polyglot/repo go test ./internal/collector/codesync/ \
//	  -run '^TestCallsLanguageCensus_EdgeProbe$' -v -count=1 -timeout 900s
//
// Set CALLS_CENSUS_PAIR_OUT to additionally dump the go->go CALLS pairs the
// POSTPAIRS line censuses, as sorted "<fromID>\t<toID>" lines.
//
// It prints two locked lines — POSTPAIRS and POSTMETHODCALLERS — each unbroken
// and with no leading whitespace, because testdata/ful1335_acceptance.txt is a
// frozen capture of them.
//
// THE "POST" PREFIX IS A LOCKED EXTERNAL TOKEN, not a description of a stage.
// It is read from outside this file: landed acceptance gates grep for these two
// names, and committed testdata artifacts are verbatim captures of them.
// Renaming either would turn those gates red against correct work and orphan
// the artifacts, so the prefix is retained for its readers rather than tidied.
//
// Diagnostics use `want:` / `got:` and never those prefixes: a failure message
// echoing a census line in the locked format could otherwise satisfy a check
// meant to read the real census.
func TestCallsLanguageCensus_EdgeProbe(t *testing.T) {
	root := os.Getenv("CALLS_CENSUS_REPO")
	if root == "" {
		t.Skip("set CALLS_CENSUS_REPO=<path to a polyglot repo with a go.mod> to run the CALLS language census")
	}
	// The env var is an untrusted path that flows into the collector's own
	// filesystem reads, so normalize it before handing it on.
	root = filepath.Clean(root)

	pop, err := parser.Populate(t.Context(), filepath.Base(root), root)
	require.NoError(t, err)

	language := censusLanguageByID(pop.Nodes)
	pairs, total := censusPairs(pop.Edges, language)
	fmt.Printf("POSTPAIRS %s\n", censusFormat(pairs))

	// (3) KNOWN-POSITIVE CONTROL. Without this the whole run could pass over a
	// corpus that produced no call edges at all, and every assertion below would
	// be vacuously true.
	callerLangs := censusCallerLanguages(pairs)
	if len(callerLangs) < 2 || total < 100 {
		t.Fatalf("want: a corpus with at least 2 caller languages and at least 100 CALLS edges; "+
			"got: %d caller languages, %d CALLS edges — the run would be vacuous",
			len(callerLangs), total)
	}

	// OPTIONAL go->go PAIR DUMP. When CALLS_CENSUS_PAIR_OUT names a path, every
	// go->go CALLS pair in the SAME edge set the POSTPAIRS line censuses is
	// written there as "<fromID>\t<toID>", one per line, sorted — so the dump and
	// the census can never describe different sets. Unset, the probe behaves
	// exactly as it did before: the dump is additive, never a required input.
	//
	// The TSV shape is carried forward from the merge-delta probe's own artifact
	// writer rather than imported from it, because that probe does not survive the
	// removal of the merge it exists to measure.
	pairOut := os.Getenv("CALLS_CENSUS_PAIR_OUT")
	if pairOut != "" {
		// Same reasoning as CALLS_CENSUS_REPO above: an operator-supplied path
		// flowing into a filesystem write is normalized before it is used.
		pairOut = filepath.Clean(pairOut)
	}
	var goPairs []string

	// (2) METHOD CALLERS. Counted here, asserted below, and printed
	// unconditionally.
	//
	// This is the corpus-scale catcher for a regression in which the analyzed
	// caller set collapses to package members: the count would fall to exactly 0
	// while assertion (1) stayed green.
	var methodCallers int
	var violations []string
	for _, e := range pop.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls {
			continue
		}
		if pairOut != "" &&
			language[e.FromId] == string(treesitter.LangGo) &&
			language[e.ToId] == string(treesitter.LangGo) {
			goPairs = append(goPairs, e.FromId+"\t"+e.ToId)
		}
		if language[e.FromId] != language[e.ToId] &&
			!sameFamilyCallPairs[[2]string{language[e.FromId], language[e.ToId]}] &&
			len(violations) < 5 {
			violations = append(violations, fmt.Sprintf("%s (%s) -> %s (%s)",
				e.FromId, language[e.FromId], e.ToId, language[e.ToId]))
		}
		if language[e.FromId] == string(treesitter.LangGo) && censusIsReceiverQualified(e.FromId) {
			methodCallers++
		}
	}
	fmt.Printf("POSTMETHODCALLERS %d\n", methodCallers)

	if pairOut != "" {
		slices.Sort(goPairs)
		//nolint:gosec // G703: pairOut is an OPERATOR-SUPPLIED path from
		// CALLS_CENSUS_PAIR_OUT on a test-only, env-gated instrument — the person
		// setting the variable is the person running the test, so it is
		// process-owned rather than request-derived, and it is Cleaned above.
		require.NoError(t, os.WriteFile(pairOut,
			[]byte(strings.Join(goPairs, "\n")+"\n"), 0o600))
	}

	// (1) NO CROSS-LANGUAGE CALLS EDGE, outside the same-family allowlist above.
	if len(violations) > 0 {
		t.Errorf("want: no CALLS edge crossing a language boundary\ngot:  %s",
			strings.Join(violations, "; "))
	}

	// (2) asserted.
	if methodCallers == 0 {
		t.Error("want: a non-empty method-caller class among the surviving Go CALLS edges; " +
			"got: 0 — methods are not being analyzed as callers")
	}
}

// censusLanguageByID indexes node ID → language over a populate result.
func censusLanguageByID(nodes []*knowledgev1.Node) map[string]string {
	byID := make(map[string]string, len(nodes))
	for _, n := range nodes {
		byID[n.Id] = n.Language
	}
	return byID
}

// censusPairs counts CALLS edges by "<fromLang>-><toLang>", and returns the
// total number of CALLS edges seen.
func censusPairs(edges []*knowledgev1.Edge, language map[string]string) (map[string]int, int) {
	pairs := make(map[string]int)
	var total int
	for _, e := range edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls {
			continue
		}
		total++
		pairs[language[e.FromId]+"->"+language[e.ToId]]++
	}
	return pairs, total
}

// censusFormat renders a census as space-separated "<pair>=<n>", ascending by
// pair name, omitting empty pairs.
func censusFormat(pairs map[string]int) string {
	names := make([]string, 0, len(pairs))
	for name, n := range pairs {
		if n > 0 {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, pairs[name]))
	}
	return strings.Join(parts, " ")
}

// censusCallerLanguages returns the set of CALLER languages present in a census.
// The caller side is the one that matters: the drop rule keys on the FROM
// endpoint, so that is where a language either survives the merge or vanishes.
func censusCallerLanguages(pairs map[string]int) map[string]bool {
	langs := make(map[string]bool)
	for name, n := range pairs {
		if n == 0 {
			continue
		}
		if from, _, found := strings.Cut(name, "->"); found {
			langs[from] = true
		}
	}
	return langs
}

// censusIsReceiverQualified reports whether a node ID's symbol half — the part
// after the LAST colon — carries a dot, which is the observable form of a
// receiver-qualified declaration, i.e. a method.
func censusIsReceiverQualified(nodeID string) bool {
	colon := strings.LastIndex(nodeID, ":")
	if colon < 0 {
		return false
	}
	return strings.Contains(nodeID[colon+1:], ".")
}
