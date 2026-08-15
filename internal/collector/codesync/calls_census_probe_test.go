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

// TestCallsLanguageCensus_EdgeProbe runs the full Populate + RTA-merge path over
// a REAL polyglot repository and censuses the CALLS edges by language on both
// sides of the merge.
//
// It is a permanent gate, not a one-off instrument: it is the only test that
// exercises the merge at corpus scale, it costs nothing when CALLS_CENSUS_REPO
// is unset, and every defect it guards was invisible to fixture-scale tests for
// as long as those defects existed.
//
// Point CALLS_CENSUS_REPO at any checkout carrying a go.mod plus at least one
// non-Go language:
//
//	CALLS_CENSUS_REPO=/path/to/polyglot/repo go test ./internal/collector/codesync/ \
//	  -run '^TestCallsLanguageCensus_EdgeProbe$' -v -count=1 -timeout 900s
//
// It prints three locked lines — PREPAIRS, POSTPAIRS and POSTMETHODCALLERS —
// each unbroken and with no leading whitespace, because testdata/ful1335_acceptance.txt
// is a frozen capture of them. Diagnostics use `want:` / `got:` and never those
// prefixes: a failure message echoing a census line in the locked format could
// otherwise satisfy a check meant to read the real census.
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

	prePairs, preTotal := censusPairs(pop.Edges, censusLanguageByID(pop.Nodes))
	fmt.Printf("PREPAIRS %s\n", censusFormat(prePairs))

	// (4) KNOWN-POSITIVE CONTROL. Without this the whole run could pass over a
	// corpus that produced no call edges at all, and every assertion below would
	// be vacuously true.
	preCallerLangs := censusCallerLanguages(prePairs)
	if len(preCallerLangs) < 2 || preTotal < 100 {
		t.Fatalf("want: a corpus with at least 2 caller languages and at least 100 CALLS edges "+
			"before the merge; got: %d caller languages, %d CALLS edges — the run would be vacuous",
			len(preCallerLangs), preTotal)
	}

	out := augmentWithPreciseCallGraph(t.Context(), pop, root)
	language := censusLanguageByID(out.Nodes)
	postPairs, _ := censusPairs(out.Edges, language)
	fmt.Printf("POSTPAIRS %s\n", censusFormat(postPairs))

	// (3) METHOD CALLERS SURVIVE THE MERGE. Counted here, asserted below, and
	// printed unconditionally.
	//
	// This is the corpus-scale catcher for the pairing of the two production
	// changes: the drop guard removes every Go-caller tree-sitter CALLS edge, so
	// if the analyzed function set ever regressed to package members this count
	// would be exactly 0 while assertions (1) and (2) stayed green.
	var methodCallers int
	var violations []string
	for _, e := range out.Edges {
		if kgtypes.EdgeType(e.Type) != kgtypes.EdgeCalls {
			continue
		}
		if language[e.FromId] != language[e.ToId] && len(violations) < 5 {
			violations = append(violations, fmt.Sprintf("%s (%s) -> %s (%s)",
				e.FromId, language[e.FromId], e.ToId, language[e.ToId]))
		}
		if language[e.FromId] == string(treesitter.LangGo) && censusIsReceiverQualified(e.FromId) {
			methodCallers++
		}
	}
	fmt.Printf("POSTMETHODCALLERS %d\n", methodCallers)

	// (1) NO CROSS-LANGUAGE CALLS EDGE.
	if len(violations) > 0 {
		t.Errorf("want: no CALLS edge crossing a language boundary\ngot:  %s",
			strings.Join(violations, "; "))
	}

	// (2) NON-GO SURVIVAL. Derived from the corpus rather than hard-coded to a
	// language list, so it cannot silently pass on a corpus that happens to lack
	// one of them. "go" is excluded because Go CALLS edges are legitimately
	// replaced wholesale by the precise call graph; every other caller language
	// must still be represented after the merge.
	postCallerLangs := censusCallerLanguages(postPairs)
	for lang := range preCallerLangs {
		if lang == string(treesitter.LangGo) {
			continue
		}
		if !postCallerLangs[lang] {
			t.Errorf("want: caller language %q still has CALLS edges after the merge\ngot:  none", lang)
		}
	}

	// (3) asserted.
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
