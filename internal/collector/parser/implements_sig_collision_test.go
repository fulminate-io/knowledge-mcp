// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// TestExternalLeafCensus is the RECORDED CONTROL for the precision exposure the
// external leaf accepts, measured over both frozen corpora.
//
// WHAT THE LEAF GIVES UP, AND WHAT IT NO LONGER DOES. A type outside the indexed
// universe renders `ext:<qualifier>.<base>`, so the aliased and non-aliased
// spellings of one import collapse — Go writes `stripe.CheckoutSession` either
// way — while two different packages declaring one type name stay apart. An
// earlier revision kept only the base name; over these corpora that merged 52
// base names on knowledge and 54 on agent, `Client` alone reachable under http,
// pubsub, gcs and eight more. Both numbers are still printed below as the
// BEFORE-measure, because what the qualifier bought is only legible against them.
//
// WHAT REMAINS EXPOSED is narrower and is what this test asserts on: two
// DIFFERENT modules imported under the SAME qualifier text still render one
// leaf. That is the residual hazard, it is measurable, and it is measured here
// rather than assumed empty.
//
// THE ASSERTION IS ON THE DEGRADATION, NOT ON THE CENSUS. A collision in the
// leaf space only spends precision where it changes a satisfaction outcome, so
// the gate is collapse_dependent_pairs — derived pairs that vanish when every
// leaf is replaced by the most specific identity available. TestCollapseControlFires
// proves that gate can return non-zero, without which a zero here would mean
// nothing.
//
//	IMPL_ROOT=$HOME/.knowledge/tsparity go test ./internal/collector/parser/ \
//	  -run '^TestExternalLeafCensus$' -v -count=1 -timeout 1800s
func TestExternalLeafCensus(t *testing.T) {
	root := os.Getenv("IMPL_ROOT")
	if root == "" {
		t.Skip("set IMPL_ROOT=<tsparity root> to run the external-leaf census")
	}
	for _, label := range []string{"knowledge", "agent"} {
		t.Run(label, func(t *testing.T) {
			corpus := filepath.Join(filepath.Clean(root), "corpora", label)
			ix, chunks := indexCorpus(t, corpus)

			var declining, qualified, unqualified int
			byBase := map[string]map[string]bool{} // BEFORE: base -> qualifier set
			byLeaf := map[string]map[string]bool{} // AFTER:  rendered leaf -> resolved scope set
			leafSpellings := map[string]bool{}
			for _, c := range chunks {
				if c.chunk.TypeFacts == nil || c.chunk.TypeFacts.Sig == nil {
					continue
				}
				for _, group := range [][]treesitter.TypeExpr{
					c.chunk.TypeFacts.Sig.Params, c.chunk.TypeFacts.Sig.Results,
				} {
					for _, e := range group {
						for _, leaf := range e.Leaves {
							if !leafRendersExt(ix, c.ref, leaf) {
								continue
							}
							declining++
							rendered := externalLeafName(c.ref, leaf)
							leafSpellings[rendered] = true

							qualifier, name := splitQualifier(c.ref.Lang, leaf)
							if qualifier == "" {
								// The no-qualifier-written population: predeclared
								// types and type parameters. Counted so the choice
								// that they carry no qualifier segment is a measured
								// one rather than an implied one.
								unqualified++
								continue
							}
							qualified++
							base := baseDeclName(name)
							if byBase[base] == nil {
								byBase[base] = map[string]bool{}
							}
							byBase[base][qualifier] = true

							// The resolved module path, where the qualifier bound.
							// An UNBOUND qualifier contributes no scope: two
							// unbound packages under one qualifier are genuinely
							// indistinguishable from here, which is stated on
							// externalLeafName as the residual's residual.
							if tr := resolveTypeText(c.ref, leaf); tr.Scope != "" {
								if byLeaf[rendered] == nil {
									byLeaf[rendered] = map[string]bool{}
								}
								byLeaf[rendered][tr.Scope] = true
							}
						}
					}
				}
			}

			// KNOWN-POSITIVE CONTROLS. Every count below is a floor and the
			// assertion is an absence; an enumeration that examined nothing would
			// satisfy it perfectly.
			require.Positivef(t, declining,
				"control: the %s corpus produced declining signature leaves at all", label)
			require.Positivef(t, qualified,
				"control: the %s corpus produced QUALIFIED declining leaves at all", label)
			require.NotEmptyf(t, byLeaf,
				"control: at least one declining leaf on %s resolved to a module path, or the "+
					"residual measure below would be vacuous", label)

			baseCollisions := collisionsOf(byBase)
			leafCollisions := collisionsOf(byLeaf)
			dependent, undecided := collapseDependentPairs(t, ix, chunks)

			fmt.Printf("EXTLEAF corpus=%s declining_leaves=%d qualified=%d unqualified=%d "+
				"distinct_rendered_leaves=%d base_name_collisions_before=%d "+
				"rendered_leaf_collisions=%d collapse_dependent_pairs=%d undecidable_pairs=%d\n",
				label, declining, qualified, unqualified,
				len(leafSpellings), len(baseCollisions), len(leafCollisions),
				len(dependent), undecided)

			// THE RESIDUAL IS REPORTED, NOT ASSERTED AT ZERO, because it is not zero
			// and the design never promised it would be. Measured: 1 on knowledge
			// (iampb.Policy under both cloud.google.com/go/iam/apiv1/iampb and
			// google.golang.org/genproto/googleapis/iam/v1) and 7 on agent (admin.*
			// across spanner and sqladmin, codes.Code across otel and grpc, genai.*
			// across vertexai and google.golang.org/genai). Every one is two modules
			// a file imported under one qualifier TEXT, which the qualifier cannot
			// separate by construction. Whether any of them actually costs a
			// satisfaction is the next assertion's job; a zero-gate here would be red
			// on arrival and would have to be weakened or ignored, which is how a
			// gate stops meaning anything.
			for _, c := range leafCollisions {
				t.Logf("EXTLEAF-RESIDUAL %s %s", label, c)
			}

			require.Emptyf(t, dependent,
				"want: no IMPLEMENTS pair on %s derived ONLY because the external leaf erased a "+
					"distinction between two types\n"+
					"got:  %d collapse-dependent pairs — each is a satisfaction the graph publishes and "+
					"the types do not have:\n%s",
				label, len(dependent), strings.Join(dependent, "\n"))
		})
	}
}

// collisionsOf renders every key reachable under two or more distinct values.
func collisionsOf(m map[string]map[string]bool) []string {
	var out []string
	for key, vals := range m {
		if len(vals) < 2 {
			continue
		}
		var listed []string
		for v := range vals {
			listed = append(listed, v)
		}
		sort.Strings(listed)
		out = append(out, key+" <- "+strings.Join(listed, ", "))
	}
	sort.Strings(out)
	return out
}

// TestCollapseControlFires is the KNOWN-POSITIVE for the census's gate.
//
// WITHOUT IT A ZERO PROVES NOTHING. collapse_dependent_pairs is an absence
// assertion over two corpora, and a control that could only ever return zero — a
// counterfactual identical to the shipped rendering, a probe pointed at nothing —
// is indistinguishable from a genuinely clean result. This fixture constructs the
// exact residual the leaf still has and requires the gate to CATCH it.
//
// THE RESIDUAL, CONSTRUCTED: two files import DIFFERENT modules under the SAME
// qualifier text `lib`. The leaf renders `ext:lib.Thing` for both, so the spec
// and the foreign implementer agree and a pair is derived; the strict
// counterfactual resolves each to its own module path, they disagree, and the
// pair disappears. That difference is precisely what the gate reports.
func TestCollapseControlFires(t *testing.T) {
	const specSrc = `package a

import lib "example.com/vendor/alpha"

type Contract interface {
	Do(x lib.Thing) error
}
`
	const implSrc = `package b

import lib "example.com/vendor/beta"

type Impl struct{}

func (Impl) Do(x lib.Thing) error { return nil }
`

	ix, chunks := indexBoundResults(t, []fixtureFile{
		{path: "a/a.go", src: specSrc},
		{path: "b/b.go", src: implSrc},
	})

	spec := recFor(t, ix, "a/a.go:Contract.Do")
	impl := recFor(t, ix, "b/b.go:Impl.Do")
	require.Equal(t, spec.SigKey, impl.SigKey,
		"fixture control: the two spellings DO collapse to one leaf — the residual this gate exists for")
	require.Equal(t, "(ext:lib.Thing)(ext:error)", spec.SigKey,
		"fixture control: and the leaf is the qualifier-plus-base form, not something else")

	dependent, _ := collapseDependentPairs(t, ix, chunks)
	require.NotEmpty(t, dependent,
		"the gate must CATCH a pair that exists only because two modules share a qualifier text; "+
			"a gate that cannot fire cannot certify the corpora")
	assert.Contains(t, strings.Join(dependent, "\n"), "a/a.go:Contract <- b/b.go:Impl")
}

// corpusChunk pairs one chunk with the reference site of its declaring file,
// which is the only site that can resolve the spellings that chunk wrote.
type corpusChunk struct {
	chunk treesitter.Chunk
	ref   *treesitter.RefSite
}

// indexCorpus runs the production front half of Populate over a corpus tree and
// returns the completed index alongside every chunk and its declaring site.
//
// IT MIRRORS Populate RATHER THAN CALLING IT because Populate returns nodes and
// edges, and this control needs the WRITTEN LEAF SPELLINGS, which live on the
// chunks and are not published in either.
func indexCorpus(t *testing.T, corpusDir string) (*declIndex, []corpusChunk) {
	t.Helper()
	files, _, err := DiscoverFilesReporting(t.Context(), corpusDir, DiscoveryOptions{})
	require.NoError(t, err)
	results, _, err := ChunkFilesParallel(t.Context(), corpusDir, files)
	require.NoError(t, err)

	mp, _ := ReadModulePath(corpusDir)
	fillBinds(&treesitter.RepoContext{Root: corpusDir, ModulePath: mp, Files: files}, results)
	DeduplicateChunks(results)

	total := 0
	for _, r := range results {
		total += len(r.Chunks)
	}
	ix := newDeclIndex(total)
	out := make([]corpusChunk, 0, total)
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			indexDeclaration(ix, r, chunk, ChunkNodeID(chunk))
			out = append(out, corpusChunk{chunk: chunk, ref: r.Ref})
		}
	}
	ix.resolveSigKeys()
	return ix, out
}

// leafRendersExt reports whether one written spelling takes resolvedLeaf's
// ext: branch. It states the branch condition ONCE, here, rather than
// re-deriving it — a control that tested a different condition than the code
// takes would be measuring nothing.
func leafRendersExt(ix *declIndex, ref *treesitter.RefSite, text string) bool {
	if _, predeclared := goPredeclaredTypes[text]; predeclared {
		return true
	}
	tr := resolveTypeText(ref, text)
	return tr.Scope == "" || !ix.hasScope(tr.Scope)
}

// collapseDependentPairs returns the IMPLEMENTS pairs that exist under the
// shipped leaf and DISAPPEAR under the most specific identity available for
// every leaf.
//
// IT RE-RUNS THE REAL DERIVATION RATHER THAN MODELING IT. Every rule the
// derivation applies — method-set coverage, package-scoped unexported names,
// embedded promotion, the generic decline — participates in both runs, so the
// difference between the two pair sets is attributable to the LEAF RENDERING and
// to nothing else.
// It also returns how many differing pairs it DROPPED as undecidable, so the
// zero it usually reports is readable against a denominator instead of being a
// bare absence.
func collapseDependentPairs(t *testing.T, ix *declIndex, chunks []corpusChunk) (pairs []string, skipped int) {
	t.Helper()
	shipped := derivedPairSet(ix)

	saved := make(map[string]string, len(ix.byID))
	for id, rec := range ix.byID {
		saved[id] = rec.SigKey
	}
	defer func() {
		for id, rec := range ix.byID {
			rec.SigKey = saved[id]
		}
	}()

	// undecidable holds the TYPE node IDs carrying at least one method whose
	// strict identity could not be determined — see strictSigKey. A pair with an
	// undecidable endpoint is dropped below rather than reported, which trades a
	// smaller denominator for an honest one.
	undecidable := map[string]bool{}
	for _, c := range chunks {
		rec, ok := ix.byID[ChunkNodeID(c.chunk)]
		if !ok || rec.SigKey == "" || c.chunk.TypeFacts == nil || c.chunk.TypeFacts.Sig == nil {
			continue
		}
		key, determinate := strictSigKey(c.ref, c.chunk.TypeFacts.Sig)
		rec.SigKey = key
		if !determinate && rec.Parent != "" {
			undecidable[rec.File+":"+rec.Parent] = true
		}
	}
	strict := derivedPairSet(ix)

	var out []string
	for p := range shipped {
		if strict[p] {
			continue
		}
		iface, concrete, found := strings.Cut(p, " <- ")
		if found && (undecidable[iface] || undecidable[concrete]) {
			skipped++
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out, skipped
}

// derivedPairSet runs the production derivation and returns its type-level pairs.
func derivedPairSet(ix *declIndex) map[string]bool {
	pairs, _ := deriveImplements(ix)
	out := make(map[string]bool, len(pairs))
	for _, p := range pairs {
		out[p.iface.NodeID+" <- "+p.concrete.NodeID] = true
	}
	return out
}

// strictSigKey renders a signature using the resolved MODULE PATH for every
// leaf, and reports whether it could do so for all of them.
//
// AN UNBOUND QUALIFIER MAKES THE SIGNATURE INDETERMINATE, and that second return
// is the whole correctness of this control. A non-aliased Go import binds under
// its path's LAST SEGMENT, so `stripe` is unbound in a file importing
// "github.com/stripe/stripe-go/v83" plainly and bound in one importing it as
// `stripe "…"`. An earlier revision of this helper wrote the module path when
// the qualifier bound and the written spelling when it did not — and thereby
// rebuilt the aliased-versus-unaliased divergence this ticket removed, INSIDE
// the instrument meant to police it. It reported all five Stripe billing pairs
// as collapse-dependent when they are the same type and the correct recall.
//
// A COUNTERFACTUAL MUST BE AT LEAST AS CONSISTENT AS THE THING IT AUDITS, not
// merely more specific: an identity whose precision varies with import spelling
// measures the spelling. Where the module cannot be known from this file the
// comparison is UNDECIDABLE, an unknown is not evidence of distinctness, and the
// caller drops the pair rather than scoring it.
func strictSigKey(ref *treesitter.RefSite, sig *treesitter.SigFacts) (key string, determinate bool) {
	var b strings.Builder
	determinate = true
	for _, group := range [][]treesitter.TypeExpr{sig.Params, sig.Results} {
		for _, e := range group {
			b.WriteString(e.Shape)
			for _, leaf := range e.Leaves {
				b.WriteString("|")
				if _, predeclared := goPredeclaredTypes[leaf]; predeclared {
					b.WriteString("ext:" + leaf)
					continue
				}
				tr := resolveTypeText(ref, leaf)
				if tr.Scope == "" {
					determinate = false
					b.WriteString("?" + leaf)
					continue
				}
				b.WriteString(tr.Scope + sigKeyLeafSep + tr.Name)
			}
		}
		b.WriteString(";")
	}
	return b.String(), determinate
}
