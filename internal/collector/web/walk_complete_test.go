// SPDX-License-Identifier: Apache-2.0

package web

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector"
)

// walk_complete_test.go — the catchers for the WALK ASSERTION this collector
// makes, and for the totality of the partition that decides it.
//
// The walk assertion is the SECOND of the two gates that arm a raw graph's
// collect-driven deletion. The first is the server's full-replace family switch;
// this one is the collector's own claim that its emission is the authoritative
// set. A collector that never sets CollectResult.WalkComplete reports an
// incomplete walk, and the server's deletion phase stays disabled however the
// family switch answers — the wrong-but-compiling implementation these tests
// exist to reject.

// walkCompleteCollectResult drives (&WebCollector{}).Collect DIRECTLY and returns
// the wire result.
//
// IT DOES NOT GO THROUGH collector.Collect, and that is load-bearing rather than
// a shortcut: the walk assertion is read off CollectResult.WalkComplete, and
// collector.Collect returns a rendered collector.CollectComposition that does not
// carry the field. Driving the method directly also means no sink is written, so
// unlike censusCollect this needs no initWebTestSink.
func walkCompleteCollectResult(t *testing.T, source string, mux *http.ServeMux) bool {
	t.Helper()

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Seeds are absolutised from srv.URL after the server starts, because its
	// address is not known until then.
	opts := CrawlOptions{
		Source:       source,
		SeedURLs:     []string{srv.URL + "/seed.html"},
		MaxDepth:     2,
		MaxPages:     10,
		PolitenessMs: 0,
	}

	res, err := (&WebCollector{}).Collect(
		WithCrawlOptions(context.Background(), opts), "", collector.CollectOptions{Force: true})
	require.NoError(t, err)
	require.NotNil(t, res)
	return res.WalkComplete
}

// walkCompleteSeedMux builds the mux both subtests serve. The seed page is
// BYTE-IDENTICAL across them and links exactly one other page, /linked.html;
// linked is the only thing that varies between the two legs.
func walkCompleteSeedMux(linked http.HandlerFunc) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/seed.html", func(w http.ResponseWriter, _ *http.Request) {
		censusPage(w, `<h1>Seed</h1>
<p>This seed page carries enough prose that the extractor keeps it rather than
discarding it as chrome, and it links exactly one other page so the crawl has a
second unit of work to either complete or fail on.</p>
<p><a href="/linked.html">the one linked page</a></p>`)
	})
	mux.HandleFunc("/linked.html", linked)
	return mux
}

// TestWebCollect_WalkCompleteReflectsTheReadFailureCensus is the PROPERTY PAIR.
//
// BOTH SUBTESTS BELONG TO THIS ONE FUNCTION and neither may be promoted to a
// sibling top-level test: the stored gate anchors its -run selector on this
// name alone, so a split would leave the selector reaching only the first while
// the other never executes and the gate went green over an unrun leg.
//
// WHY THE PAIR AND NOT ONE LEG. Each leg alone is satisfied by a wrong
// implementation. With only the failure leg, a collector hardcoding false
// passes; with only the clean leg, one hardcoding true passes. Together they
// pin the flag to the read-failure census, and this is the only gate in this
// changeset that separates the real web fix from the inert one — the family
// switch alone turns every server-side convergence test green.
func TestWebCollect_WalkCompleteReflectsTheReadFailureCensus(t *testing.T) {
	// The clean control. Without it a collector that never asserts a complete
	// walk — today's behavior — would satisfy the failure leg and nothing else.
	t.Run("clean_crawl_asserts_a_complete_walk", func(t *testing.T) {
		mux := walkCompleteSeedMux(func(w http.ResponseWriter, _ *http.Request) {
			censusPage(w, `<h1>Linked</h1>
<p>The linked page answers normally, so nothing this crawl set out to read went
unread and the emission is the authoritative set for this configuration.</p>`)
		})
		assert.True(t, walkCompleteCollectResult(t, "walk-clean", mux),
			"a crawl that read every unit it set out to read must assert a COMPLETE walk — "+
				"without it the server's deletion phase stays disabled and the graph accumulates every generation")
	})

	// The failure leg. It hijacks the connection and closes it with no response,
	// the shape crawl_degrade_census_test.go already uses to reach fetch_failed.
	// Nothing else varies: not the seed bytes, not the options, not the depth.
	t.Run("fetch_failure_clears_the_walk_assertion", func(t *testing.T) {
		mux := walkCompleteSeedMux(func(w http.ResponseWriter, _ *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				return
			}
			_ = conn.Close()
		})
		assert.False(t, walkCompleteCollectResult(t, "walk-fetch-failure", mux),
			"a unit this crawl SET OUT TO READ was missing because reading it FAILED, so the "+
				"emission is NOT known to be the authoritative set and the walk assertion must be cleared")
	})
}

// TestWebDegradeVocabulary_EveryClassIsClassifiedForTheWalkAssertion is the
// PARTITION PARITY test.
//
// WHAT IT EXISTS TO PREVENT. walkCompleteFrom answers by consulting the
// read-failure list, so a degrade class in NEITHER list reads as "not a read
// failure" — the walk-complete, deletion-ENABLING answer. That is the opposite
// of the wire field's own documented default, where the zero value is the safe
// one. The vocabulary is a block of individual string constants with no
// enumerable set, so without this test nothing forces a decision on a new
// member, and the unsafe direction is the one a new member defaults into.
//
// IT DERIVES ITS EXPECTATION FROM SOURCE RATHER THAN RESTATING IT. The landed
// house precedent for a vocabulary-parity test hand-lists the declared set,
// which cannot notice a const missing from both the switch and the test's own
// copy. Parsing the package's const declarations removes that hole: the
// authoritative declaration IS the expectation, so a class the author never told
// this test about is still found.
func TestWebDegradeVocabulary_EveryClassIsClassifiedForTheWalkAssertion(t *testing.T) {
	declared := parseDeclaredDegradeClasses(t)
	declaredValues := map[string]bool{}
	for _, v := range declared {
		declaredValues[v] = true
	}

	// INSTRUMENT CONTROL FIRST. A parser that found nothing would make every
	// assertion below vacuously true, and this known-positive is what separates
	// "the partition is total" from "the census read zero files".
	assert.Contains(t, declaredValues, "fetch_failed",
		"the const parse found no known class — every assertion below would be vacuous")

	classified := map[string]string{}
	for _, c := range readFailureDegradeClasses {
		assert.NotContains(t, classified, c,
			"%q appears twice across the two classification lists — the partition must be DISJOINT", c)
		classified[c] = "readFailureDegradeClasses"
	}
	for _, c := range policyDegradeClasses {
		assert.NotContains(t, classified, c,
			"%q appears twice across the two classification lists — the partition must be DISJOINT", c)
		classified[c] = "policyDegradeClasses"
	}

	// TOTALITY: every declared class is classified. An unclassified class reads as
	// "not a read failure", which is the deletion-ENABLING answer.
	for id, value := range declared {
		assert.Contains(t, classified, value,
			"degrade class %q (const %s) is in NEITHER readFailureDegradeClasses nor "+
				"policyDegradeClasses. An unclassified class defaults to 'not a read failure', so a "+
				"crawl that hit it would still assert a COMPLETE walk and the server would delete "+
				"every node the crawl did not re-emit. Classify it: a READ FAILURE is a unit this "+
				"crawl set out to read that is missing because reading it FAILED; anything else is a "+
				"scoping or policy decision this collector made deliberately.", value, id)
	}

	// THE MIRROR: a classified entry naming a const the package no longer declares
	// is just as wrong, and would otherwise sit undetected forever.
	for c, list := range classified {
		assert.Contains(t, declaredValues, c,
			"%s names %q, which no degrade const in this package declares — a stale entry", list, c)
	}

	// The cardinality equality is what catches a duplicate masking a missing member.
	assert.Len(t, classified, len(declaredValues),
		"the classified set and the declared set must have equal cardinality")
}

// parseDeclaredDegradeClasses reads every NON-TEST .go file in this package
// directory and returns the declared degrade vocabulary as const identifier ->
// string value. It is the authoritative set the parity test measures against.
func parseDeclaredDegradeClasses(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	out := map[string]string{}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		require.NoError(t, err, "parsing %s", e.Name())

		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "degrade") || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					require.NoError(t, err, "unquoting %s", name.Name)
					out[name.Name] = v
				}
			}
		}
	}
	require.NotEmpty(t, out, "no degrade consts parsed from %d entries", len(entries))
	return out
}
