// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/treesitter"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

const (
	// auditArtifact is the spot-audit sample a reader opens against corpus
	// source. It lives in /tmp for the reason the corpus artifact does:
	// committing it would ship absolute personal paths.
	auditArtifact = "/tmp/f1396_implements_sample.json"

	// auditSampleSize is the per-language cap. It is a FIXED literal rather
	// than a fraction, because the instrument is a bounded HUMAN read and the
	// bound has to be one a person actually completes.
	auditSampleSize = 25
)

// auditEdge is one sampled relationship, carrying enough to open BOTH ends in
// corpus source without a second query — which is the whole point of the
// artifact, since an audit that cannot reach the source can only transcribe.
type auditEdge struct {
	FromID   string `json:"from_id"`
	ToID     string `json:"to_id"`
	FromFile string `json:"from_file"`
	ToFile   string `json:"to_file"`
	FromLine int    `json:"from_line"`
	ToLine   int    `json:"to_line"`
	Method   string `json:"method"`
}

// auditLang is one language's sample and the population it was drawn from.
//
// TOTAL IS RECORDED BESIDE SAMPLED so a reader can tell a small sample from a
// small population. A language producing zero is recorded AS zero rather than
// omitted: an omitted row and a zero row read identically, and only one of them
// is a fact.
type auditLang struct {
	Sampled int         `json:"sampled"`
	Total   int         `json:"total"`
	Edges   []auditEdge `json:"edges"`
}

// auditJSON is the artifact envelope.
type auditJSON struct {
	Commit    string                `json:"commit"`
	Languages map[string]*auditLang `json:"languages"`
}

// auditLoc is one declaration's source location.
type auditLoc struct {
	file string
	line int
}

// TestFUL1396ImplementsSpotAudit samples the DECLARED-CONFORMANCE relationships
// the armed languages produce and writes them out for a source read.
//
// THERE IS NO ORACLE FOR THESE LANGUAGES, which is exactly why the instrument is
// a bounded human read rather than a score. What this test produces is the
// sample; what the audit produces is the miss count, read against source, and
// the bar for that count is ZERO.
func TestFUL1396ImplementsSpotAudit(t *testing.T) {
	raw := os.Getenv(multiLangRootsEnv)
	if raw == "" {
		t.Skipf("set %s to a %c-separated list of corpus roots to sample real edges",
			multiLangRootsEnv, os.PathListSeparator)
	}

	out := auditJSON{Commit: knowledgeHeadCommit(t), Languages: map[string]*auditLang{}}
	// EVERY ARMED LANGUAGE GETS A ROW BEFORE ANY MEASUREMENT, so a language that
	// produced nothing is reported as a zero rather than disappearing.
	for _, lang := range treesitter.NominalArmedLanguages() {
		out.Languages[string(lang)] = &auditLang{}
	}

	for _, root := range filepath.SplitList(raw) {
		if root == "" {
			continue
		}
		auditCorpus(t, filepath.Clean(root), out.Languages)
	}

	for _, entry := range out.Languages {
		sort.Slice(entry.Edges, func(i, j int) bool {
			if entry.Edges[i].FromID != entry.Edges[j].FromID {
				return entry.Edges[i].FromID < entry.Edges[j].FromID
			}
			return entry.Edges[i].ToID < entry.Edges[j].ToID
		})
		entry.Total = len(entry.Edges)
		entry.Edges = auditSample(entry.Edges)
		entry.Sampled = len(entry.Edges)
	}

	body, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(auditArtifact, append(body, '\n'), 0o600))
	for lang, entry := range out.Languages {
		t.Logf("%s: sampled %d of %d", lang, entry.Sampled, entry.Total)
	}
}

// auditSample takes every k-th edge up to the cap.
//
// THE STRIDE IS DETERMINISTIC AND SPREAD, not a head slice. A head slice of a
// list sorted by node ID samples one directory of one package, which would let
// a defect confined to any other part of the corpus pass an audit unseen.
func auditSample(edges []auditEdge) []auditEdge {
	if len(edges) <= auditSampleSize {
		return edges
	}
	stride := len(edges) / auditSampleSize
	out := make([]auditEdge, 0, auditSampleSize)
	for i := 0; i < len(edges) && len(out) < auditSampleSize; i += stride {
		out = append(out, edges[i])
	}
	return out
}

// auditCorpus measures one root and appends its declared-conformance
// relationships, attributed to the SUBTYPE's language — the declaration that
// wrote the clause, and whose capture arm read it.
func auditCorpus(t *testing.T, root string, langs map[string]*auditLang) {
	t.Helper()
	require.True(t, filepath.IsAbs(root), "%s entries must be absolute paths, got %q", multiLangRootsEnv, root)

	ctx := context.Background()
	files, err := DiscoverFiles(ctx, root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "control: discovery found no files under %s", root)

	results, _, err := ChunkFilesParallel(ctx, root, files)
	require.NoError(t, err)
	require.NotEmpty(t, results, "control: chunking produced no results under %s", root)

	DeduplicateChunks(results)
	resolveSlotEdges(results)
	modulePath, _ := ReadModulePath(root)
	rc := treesitter.RepoContext{Root: root, ModulePath: modulePath, Files: files}
	fillBinds(&rc, results)

	ix := newDeclIndex(0)
	loc := map[string]auditLoc{}
	owner := map[string]string{}
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			id := ChunkNodeID(chunk)
			loc[id] = auditLoc{file: r.FilePath, line: chunk.StartLine}
			owner[id] = string(r.Language)
			indexDeclaration(ix, r, chunk, id)
		}
	}

	// Owners are stamped exactly where the production path stamps them — after
	// the index is complete — because the member pairing this audit samples
	// reads them.
	stampDeclOwners(ix, results)

	for _, e := range emitDeclaredConformanceEdges(ix) {
		entry, ok := langs[owner[e.ToId]]
		if !ok {
			// A language outside this group's armed set contributed the
			// relationship. It is not this audit's subject and is skipped rather
			// than filed under a row it does not belong to.
			continue
		}
		entry.Edges = append(entry.Edges, auditEdge{
			FromID:   e.FromId,
			ToID:     e.ToId,
			FromFile: loc[e.FromId].file,
			ToFile:   loc[e.ToId].file,
			FromLine: loc[e.FromId].line,
			ToLine:   loc[e.ToId].line,
			Method:   e.Method,
		})
	}
}

// TestFUL1396MemberOwnerConsistency asserts, over EVERY member relationship the
// corpus produces, that each endpoint is owned by the declaration the
// relationship names.
//
// IT EXISTS BECAUSE A SAMPLE CANNOT FIND THIS CLASS OF DEFECT. The spot audit
// reads 25 edges per language against source, which is the right instrument for
// direction, endpoints and clause kind — but a crossing that affects two
// relationships in a thousand is invisible to it by construction, and exactly
// that ratio is what the pinned scala corpus carried. This is the complement: a
// mechanical scan of the WHOLE population, checking the one property a machine
// can decide.
//
// THE PROPERTY IS THE PAIRING RULE ITSELF, restated as an invariant over
// output: a member relationship runs from a member of the supertype to a member
// of the subtype, so each endpoint's owner is that declaration — or a co-owning
// body of it, where the language defines two containers as one type. A record
// whose owner could not be addressed at all is skipped, because there is
// nothing to check rather than nothing wrong.
func TestFUL1396MemberOwnerConsistency(t *testing.T) {
	raw := os.Getenv(multiLangRootsEnv)
	if raw == "" {
		t.Skipf("set %s to a %c-separated list of corpus roots to scan every member relationship",
			multiLangRootsEnv, os.PathListSeparator)
	}

	checked, unowned := 0, 0
	for _, root := range filepath.SplitList(raw) {
		if root == "" {
			continue
		}
		ix := auditIndexFor(t, filepath.Clean(root))
		pairs, _ := deriveDeclaredConformance(ix)
		for _, p := range pairs {
			for _, m := range p.members {
				for _, side := range []struct {
					member    *declRec
					container *declRec
					role      string
				}{
					{m.spec, p.supertype, "supertype"},
					{m.impl, p.subtype, "subtype"},
				} {
					if side.member.Owner == "" {
						unowned++
						continue
					}
					checked++
					if side.member.Owner == side.container.NodeID {
						continue
					}
					require.Truef(t, conformCoOwner(side.container, ix.byID[side.member.Owner]),
						"%s member %s is owned by %s, which is neither %s nor a co-owning body of it",
						side.role, side.member.NodeID, side.member.Owner, side.container.NodeID)
				}
			}
		}
	}

	// KNOWN-POSITIVE CONTROL. Every assertion above is a property of a
	// population, and an empty population satisfies all of them — so the scan
	// must be shown to have read a real one.
	require.Positive(t, checked,
		"control: member relationships were produced and their owners actually examined")
	t.Logf("owner-consistent endpoints checked=%d, unaddressable-owner endpoints skipped=%d", checked, unowned)
}

// auditIndexFor builds one root's declaration index through the production
// order, owners included.
func auditIndexFor(t *testing.T, root string) *declIndex {
	t.Helper()
	ctx := context.Background()
	files, err := DiscoverFiles(ctx, root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "control: discovery found files under %s", root)

	results, _, err := ChunkFilesParallel(ctx, root, files)
	require.NoError(t, err)
	require.NotEmpty(t, results, "control: chunking produced results under %s", root)

	DeduplicateChunks(results)
	resolveSlotEdges(results)
	modulePath, _ := ReadModulePath(root)
	rc := treesitter.RepoContext{Root: root, ModulePath: modulePath, Files: files}
	fillBinds(&rc, results)

	ix := newDeclIndex(0)
	for _, r := range results {
		for _, chunk := range r.Chunks {
			if kgtypes.NodeType(chunk.ChunkType).IsComment() {
				continue
			}
			indexDeclaration(ix, r, chunk, ChunkNodeID(chunk))
		}
	}
	stampDeclOwners(ix, results)
	return ix
}
