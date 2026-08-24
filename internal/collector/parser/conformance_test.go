// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
)

// conformFile is one hand-built file's worth of declarations for the
// conformance tests, indexed through the production indexDeclaration path.
type conformFile struct {
	path  string
	lang  treesitter.Language
	decls []gateDecl
}

// conformIndex builds a complete declaration index from hand-built files.
//
// The index is built by hand because no language arm fills TypeFacts.Conforms
// yet — the carrier is what this work lands, and the arms that fill it are
// separate work. Setting the facts directly is the only way to exercise the
// path the first arm will take, and it goes through indexDeclaration so nothing
// here can drift from what a collect actually builds.
func conformIndex(files ...conformFile) *declIndex {
	ix := newDeclIndex(8)
	for _, f := range files {
		gateIndexOf(ix, f.path, f.lang, f.decls)
	}
	return ix
}

// conformFacts builds type facts for a declaration that may declare supertypes.
func conformFacts(iface bool, sup ...treesitter.DeclaredSupertype) *treesitter.TypeFacts {
	return &treesitter.TypeFacts{IsInterface: iface, Conforms: sup}
}

// declared is one declared supertype, written as the source wrote it.
func declared(text string, kind treesitter.ConformanceKind) treesitter.DeclaredSupertype {
	return treesitter.DeclaredSupertype{Text: text, Kind: kind}
}

// gateFindEdge returns the one emitted edge between two endpoints, failing
// loudly when it is absent rather than returning a zero value an assertion
// would then read fields off.
func gateFindEdge(t *testing.T, edges []*knowledgev1.Edge, from, to string) *knowledgev1.Edge {
	t.Helper()
	for _, e := range edges {
		if e.FromId == from && e.ToId == to {
			return e
		}
	}
	require.FailNowf(t, "edge not emitted", "no edge %s -> %s in the emitted set", from, to)
	return nil
}

// TestF0ConformanceCapture covers the CAPTURE stage on its own: what the record
// carries out of index-build time, before anything has been resolved.
//
// Every subtest here is capture-time and none touches an index, which is the
// property under test as much as the values are — capture reads no index, so
// the half-built-index hazard is unreachable by construction rather than by
// ordering care.
func TestF0ConformanceCapture(t *testing.T) {
	t.Run("text_and_kind_verbatim", func(t *testing.T) {
		// A qualified spelling with its type arguments already stripped by the
		// arm. The qualifier and the leading namespace separator must SURVIVE:
		// binding a name to a scope is resolution's job, against the declaring
		// file's imports, and a capture that normalized the qualifier away would
		// destroy the only input that job has.
		got := captureDeclConforms([]treesitter.DeclaredSupertype{
			{Text: "\\App\\Contracts\\Greeter", Kind: treesitter.ConformImplements},
			{Text: "other.Base", Kind: treesitter.ConformExtends},
			{Text: "Loggable", Kind: treesitter.ConformMixin},
		})
		require.Equal(t, []conformRef{
			{Text: "\\App\\Contracts\\Greeter", Kind: treesitter.ConformImplements},
			{Text: "other.Base", Kind: treesitter.ConformExtends},
			{Text: "Loggable", Kind: treesitter.ConformMixin},
		}, got, "capture must copy the spelling and the kind verbatim, in order")
	})

	t.Run("empty_text_dropped", func(t *testing.T) {
		// An empty spelling names nothing and could only ever resolve to
		// nothing, so carrying it forward would inflate the emitter's
		// unresolvable count with entries that were never a supertype.
		got := captureDeclConforms([]treesitter.DeclaredSupertype{
			{Text: "", Kind: treesitter.ConformImplements},
			{Text: "Greeter", Kind: treesitter.ConformImplements},
			{Text: "", Kind: treesitter.ConformUndeclared},
		})
		// KNOWN-POSITIVE CONTROL in the same call: the surviving entry proves
		// the drop is selective rather than a capture that returns nothing.
		require.Equal(t, []conformRef{{Text: "Greeter", Kind: treesitter.ConformImplements}}, got,
			"only the empty-Text entries may be dropped")

		require.Nil(t, captureDeclConforms([]treesitter.DeclaredSupertype{{Text: ""}}),
			"an input of nothing but empty spellings must capture as nil, not as an empty non-nil slice")
	})

	t.Run("nil_conforms_is_nil", func(t *testing.T) {
		require.Nil(t, captureDeclConforms(nil),
			"nil means \"this declaration declares no supertype\" and must round-trip as nil")
		require.Nil(t, captureDeclConforms([]treesitter.DeclaredSupertype{}),
			"an empty list is the same answer as nil and must not allocate")
	})
}

// TestF0ConformanceResolution covers the RESOLVE stage, which runs in the
// emitter against a COMPLETE index rather than while the index is being built.
func TestF0ConformanceResolution(t *testing.T) {
	t.Run("resolves_same_file", func(t *testing.T) {
		const (
			path = "app/local.py"
			sup  = path + ":Greeter"
			sub  = path + ":Server"
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangPython, decls: []gateDecl{
			{nodeID: sup, name: "Greeter", facts: conformFacts(true)},
			{nodeID: sub, name: "Server", facts: conformFacts(false, declared("Greeter", treesitter.ConformImplements))},
		}})

		require.True(t, gateHasEdge(emitDeclaredConformanceEdges(ix), sup, sub),
			"a supertype declared in the same file must resolve against the declaring file's own site")
	})

	t.Run("unresolvable_counted", func(t *testing.T) {
		const (
			path = "app/remote.py"
			sub  = path + ":Client"
			sup  = path + ":Greeter"
			ok   = path + ":Server"
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangPython, decls: []gateDecl{
			{nodeID: sub, name: "Client", facts: conformFacts(false, declared("vendor.Missing", treesitter.ConformExtends))},
			// KNOWN-POSITIVE CONTROL in the same index, so a non-zero count
			// cannot come from an emitter that resolves nothing at all.
			{nodeID: sup, name: "Greeter", facts: conformFacts(true)},
			{nodeID: ok, name: "Server", facts: conformFacts(false, declared("Greeter", treesitter.ConformImplements))},
		}})

		_, stats := deriveDeclaredConformance(ix)
		require.Positive(t, stats.Unresolvable,
			"a supertype naming nothing in-repo must drive the unresolvable count, which is what carries that outcome now that no index-time flag records it")
		require.Equal(t, 2, stats.Supertypes, "control: both declared supertypes were seen")
		require.Equal(t, 1, stats.TypePairs, "control: the in-repo supertype still produced its pair")
	})

	t.Run("predeclared_name_is_not_dropped", func(t *testing.T) {
		// THE CATCHER against routing this resolution through the embeds
		// resolver, which drops every spelling in Go's universe block. Other
		// languages legitimately declare real types with those names, so
		// applying Go's universe to another language's supertype spelling would
		// silently report an in-repo conformance as external.
		//
		// TWO LEGS, and the SECOND is the one that can fail. The universe set is
		// Go's own LOWERCASE spelling list, so a capitalized `Any` is not a
		// member of it and that leg cannot detect the wrong routing on its own;
		// `error` IS a member, and it is exactly the case a language declaring a
		// real type of that name would hit.
		const (
			path    = "app/universe.rs"
			anyIfc  = path + ":Any"
			anySub  = path + ":Boxed"
			errIfc  = path + ":error"
			errSub  = path + ":ParseError"
			wantAll = 2
		)
		ix := conformIndex(conformFile{path: path, lang: treesitter.LangRust, decls: []gateDecl{
			{nodeID: anyIfc, name: "Any", facts: conformFacts(true)},
			{nodeID: anySub, name: "Boxed", facts: conformFacts(false, declared("Any", treesitter.ConformTrait))},
			{nodeID: errIfc, name: "error", facts: conformFacts(true)},
			{nodeID: errSub, name: "ParseError", facts: conformFacts(false, declared("error", treesitter.ConformTrait))},
		}})

		edges := emitDeclaredConformanceEdges(ix)
		require.True(t, gateHasEdge(edges, anyIfc, anySub),
			"a supertype spelled Any must resolve to the in-repo declaration of that name")
		require.True(t, gateHasEdge(edges, errIfc, errSub),
			"a supertype spelled with a name in Go's universe block must still resolve for a non-Go declaration: this resolution is a SIBLING of the embeds rule, never a call to it")
		require.Len(t, edges, wantAll, "both supertypes resolve; neither is dropped")
	})

	t.Run("resolves_cross_file_through_binds", func(t *testing.T) {
		// THE CONFORMANCE CONSUMER'S WIRING CATCHER. The supertype is declared
		// in the OTHER file and the subtype's file reaches it only through an
		// import bind, so the index-blind answer names nothing the index
		// declares. If the emitter still calls the index-blind resolver, THIS
		// SUBTEST AND ONLY THIS SUBTEST goes red — a catcher on the other
		// consumer of the same helper would stay green.
		const (
			supPath = "web/greeter.ts"
			subPath = "web/server.ts"
			sup     = supPath + ":Greeter"
			sub     = subPath + ":Server"
		)
		ix := newDeclIndex(4)
		gateIndexOf(ix, supPath, treesitter.LangTypeScript, []gateDecl{
			{nodeID: sup, name: "Greeter", facts: conformFacts(true)},
		})
		// The subtype's own site carries the bind its import established. Built
		// here rather than chunked because no arm fills TypeFacts.Conforms yet,
		// so the declared supertype has to be supplied directly.
		subResult := &treesitter.Result{
			FilePath: subPath,
			Language: treesitter.LangTypeScript,
			Ref: &treesitter.RefSite{
				File:  subPath,
				Scope: treesitter.ScopeID(subPath, treesitter.LangTypeScript, ""),
				Lang:  treesitter.LangTypeScript,
				Binds: map[string]treesitter.Bind{
					"Greeter": {Scope: treesitter.ScopeID(supPath, treesitter.LangTypeScript, "")},
				},
			},
		}
		indexDeclaration(ix, subResult, treesitter.Chunk{
			FilePath:  subPath,
			Language:  treesitter.LangTypeScript,
			Name:      "Server",
			TypeFacts: conformFacts(false, declared("Greeter", treesitter.ConformImplements)),
		}, sub)

		require.NotEqual(t,
			treesitter.ScopeID(supPath, treesitter.LangTypeScript, ""),
			treesitter.ScopeID(subPath, treesitter.LangTypeScript, ""),
			"control: the two files must be in DIFFERENT scopes, or this proves nothing about crossing one")

		require.True(t, gateHasEdge(emitDeclaredConformanceEdges(ix), sup, sub),
			"a supertype declared in another file and reached through this file's import bind must resolve: the emitter has to consult the index-aware resolver, not the index-blind one")
	})
}
