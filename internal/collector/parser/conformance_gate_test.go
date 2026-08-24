// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// gateDecl is one hand-built declaration for the gate reproduction: the node ID
// the record takes, its name, its container, and the type facts it carries.
//
// The index is built by hand rather than by chunking a fixture because the
// hazard is about a language that reaches NO type-facts arm at all. Chunking a
// python file produces no TypeFacts, so the state under test would never arise;
// setting Chunk.TypeFacts directly is the only way to reproduce what the FIRST
// registered non-Go arm will hand the index. Chunk.TypeFacts is exported, so
// this needs no chunker, no registry manipulation and therefore carries none of
// the disarm-the-production-arm hazard RegisterTypeFacts' doc comment warns of.
type gateDecl struct {
	nodeID string
	name   string
	parent string
	facts  *treesitter.TypeFacts
}

// gateIndexOf indexes one language's declarations into an existing index,
// through the PRODUCTION path — indexDeclaration — so nothing here can drift
// from what a collect actually builds.
func gateIndexOf(ix *declIndex, path string, lang treesitter.Language, decls []gateDecl) {
	result := &treesitter.Result{
		FilePath: path,
		Language: lang,
		Ref: &treesitter.RefSite{
			File:  path,
			Scope: treesitter.ScopeID(path, lang, ""),
			Lang:  lang,
		},
	}
	for _, d := range decls {
		indexDeclaration(ix, result, treesitter.Chunk{
			FilePath:   path,
			Language:   lang,
			Name:       d.name,
			ParentName: d.parent,
			TypeFacts:  d.facts,
		}, d.nodeID)
	}
}

// gateSig is the signature both Go control declarations carry. Its leaves are
// PREDECLARED spellings, so each renders `ext:` and the interface spec and the
// concrete method resolve to the SAME non-empty key — which is the only reason
// the control's pair is derived.
func gateSig() *treesitter.SigFacts {
	return &treesitter.SigFacts{
		Params:  []treesitter.TypeExpr{{Shape: treesitter.TypeExprLeafSep, Leaves: []string{"string"}}},
		Results: []treesitter.TypeExpr{{Shape: treesitter.TypeExprLeafSep, Leaves: []string{"error"}}},
	}
}

// TestF0GoDerivationIsGoOnly reproduces, and then pins, the fact that the
// method-set derivation runs for EVERY language rather than for Go.
//
// THE HAZARD. implIndexViews walks the whole index with no language filter,
// implInterfaceKeys selects on IsInterface alone, and implMatch's rule (a)
// compares SigKeys with `!=` — so TWO EMPTY SIGNATURE KEYS COMPARE EQUAL. A
// language whose arm sets IsInterface but composes no signature therefore does
// not derive nothing; it derives a large volume of unfounded pairs, braked only
// by the unexported-confinement rule, which keys on Go's capitalization
// convention and collapses to same-scope confinement for every lowercase-method
// language. Go's scoping today is ACCIDENTAL: it holds solely because the Go
// arm is the only registered type-facts arm.
//
// THE FAILURE TO EXPECT, before the gate lands, is a NON-ZERO pair count for a
// non-Go language. It is never a nil dereference and never a build error — this
// reproduction compiles against the ungated tree and names no field the gate
// adds.
func TestF0GoDerivationIsGoOnly(t *testing.T) {
	ix := newDeclIndex(8)

	// THE NON-GO QUARTET. python reaches no type-facts arm today, so these facts
	// are exactly the shape the first registered non-Go arm will produce: an
	// interface marker, members, and NO Sig at all — deferSigKey returns early on
	// the nil, so every one of these keys stays EMPTY even after resolveSigKeys.
	const (
		pyPath  = "svc/dispatch.py"
		pySink  = pyPath + ":Sink"
		pyWrite = pyPath + ":Sink.write"
		pyWrtr  = pyPath + ":Writer"
		pyImpl  = pyPath + ":Writer.write"
	)
	gateIndexOf(ix, pyPath, treesitter.LangPython, []gateDecl{
		{nodeID: pySink, name: "Sink", facts: &treesitter.TypeFacts{IsInterface: true}},
		{nodeID: pyWrite, name: "write", parent: "Sink", facts: &treesitter.TypeFacts{}},
		{nodeID: pyWrtr, name: "Writer", facts: &treesitter.TypeFacts{IsInterface: false}},
		{nodeID: pyImpl, name: "write", parent: "Writer", facts: &treesitter.TypeFacts{}},
	})

	// THE GO CONTROL, in the SAME index and the same run. Its names and its
	// signature are deliberately unrelated to the non-Go quartet's, so neither
	// side can satisfy the other's assertion.
	const (
		goPath  = "svc/store.go"
		goRepo  = goPath + ":Repo"
		goSpec  = goPath + ":Repo.Fetch"
		goMem   = goPath + ":MemRepo"
		goFetch = goPath + ":MemRepo.Fetch"
	)
	gateIndexOf(ix, goPath, treesitter.LangGo, []gateDecl{
		{nodeID: goRepo, name: "Repo", facts: &treesitter.TypeFacts{IsInterface: true}},
		{nodeID: goSpec, name: "Fetch", parent: "Repo", facts: &treesitter.TypeFacts{Sig: gateSig()}},
		{nodeID: goMem, name: "MemRepo", facts: &treesitter.TypeFacts{IsInterface: false}},
		{nodeID: goFetch, name: "Fetch", parent: "MemRepo", facts: &treesitter.TypeFacts{Sig: gateSig()}},
	})

	// MANDATORY, AND THE EASIEST THING HERE TO GET SILENTLY WRONG.
	// indexDeclaration no longer assigns SigKey at all — it defers, and
	// resolveSigKeys renders every pending key. The production path calls this on
	// the line immediately before emitImplementsEdges, so a harness that skips it
	// is not reproducing the production state: every key would stay empty, the Go
	// control's pair would be derived for the empty-equals-empty reason rather
	// than the matching-signature one, and BOTH subtests would be green while
	// proving nothing.
	ix.resolveSigKeys()

	// THE CATCHER for an omitted resolveSigKeys call. Without this assertion the
	// omission above is silent; with it, the omission goes red on a clear
	// message before either subtest runs.
	require.NotEmpty(t, recFor(t, ix, goSpec).SigKey,
		"the Go control's resolved SigKey is empty: resolveSigKeys did not run, so every key below compares empty-to-empty and the control proves nothing")
	require.NotEmpty(t, recFor(t, ix, goFetch).SigKey,
		"the Go control's concrete SigKey is empty: resolveSigKeys did not run, so every key below compares empty-to-empty and the control proves nothing")

	edges := emitImplementsEdges(ix)

	t.Run("non_go_derives_nothing", func(t *testing.T) {
		nonGo := map[string]bool{pySink: true, pyWrite: true, pyWrtr: true, pyImpl: true}
		var touched []string
		for _, e := range edges {
			if nonGo[e.FromId] || nonGo[e.ToId] {
				touched = append(touched, e.FromId+" -> "+e.ToId)
			}
		}
		require.Emptyf(t, touched,
			"the method-set derivation produced %d edge(s) touching a non-Go declaration: %v — the failure to expect here is a NON-ZERO pair count for a non-Go language, never a nil dereference and never a build error. Go's derivation compares SigKeys with != and a language that composes no signature leaves every key empty, so empty compares equal to empty and unfounded pairs are derived",
			len(touched), touched)
	})

	t.Run("go_control_still_derives", func(t *testing.T) {
		// The known-positive control: without it, a gate that disabled the
		// derivation outright would satisfy the subtest above and this test
		// would be asserting nothing about the discrimination it exists to make.
		require.True(t, gateHasEdge(edges, goRepo, goMem),
			"the Go type-level IMPLEMENTS edge is missing: the gate must leave Go's own derivation byte-identical, not disable the derivation")
		require.True(t, gateHasEdge(edges, goSpec, goFetch),
			"the Go method-level IMPLEMENTS edge is missing: the gate must leave Go's own derivation byte-identical, not disable the derivation")
	})
}

// gateHasEdge reports whether the emitted set carries one directed edge.
func gateHasEdge(edges []*knowledgev1.Edge, from, to string) bool {
	for _, e := range edges {
		if e.FromId == from && e.ToId == to {
			return true
		}
	}
	return false
}
