// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/contribhash"
)

// collectorOutputCorpusRoot is the 22-language checked-in corpus the test-kind
// coverage matrix already maintains. It is used rather than testdata/f2corpus
// (four languages) because this gate must see every registered language arm, and
// rather than an env-gated external corpus because the gate is always-on.
const collectorOutputCorpusRoot = "../treesitter/testdata/test_kind"

// pinnedCollectorOutputDigest is the identity of what the collector EMITS over
// that corpus. It moves when the collector's output moves, and only then.
//
// WHY THIS EXISTS BESIDE THE PER-FILE CONTRIBUTION HASH RATHER THAN INSIDE IT.
// The per-file hash covers 14 node fields and 7 edge fields and is already a
// function of chunker output, so a digest mirroring that tuple would duplicate an
// existing check and detect nothing new. FOUR EMITTED VALUES SIT OUTSIDE IT and
// they are the point of this gate: node Id (docs/collect-contribution-hash.md
// :79-85), Summary and Keywords (:199-214 — the server DURABLY OVERWRITES both
// with LLM values, so hashing them per-file would mark every enriched file
// changed forever) and metadata. A change to any of those four leaves every
// per-file hash identical, so a diff-mode collect reads the affected files as
// unchanged and never re-uploads them. Nothing else in the system notices.
const pinnedCollectorOutputDigest = "62cbd027bd3805a24a10e9b1800efca9a22bdb61f483bbb98446732907415790"

// setDiff returns the sorted members of a that are absent from b.
func setDiff(a, b map[string]struct{}) []string {
	out := make([]string, 0, len(a))
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestCollectorOutputIdentity_Digest pins the collector's full emitted payload
// across the corpus, so any change to what it emits goes red demanding a
// CollectorOutputVersion bump.
//
// IT ALSO CROSS-CHECKS ITS OWN COVERAGE. The distinct languages observed in the
// emitted nodes and the language directories on disk are two INDEPENDENT
// measurements of the same quantity; neither is written here as a literal. A
// corpus that silently collapsed, or a populate pass that silently stopped
// registering an arm, disagrees with the directory count and fails here rather
// than quietly pinning a digest over less than it claims to cover.
func TestCollectorOutputIdentity_Digest(t *testing.T) {
	root, err := filepath.Abs(collectorOutputCorpusRoot)
	require.NoError(t, err)

	pop, err := Populate(context.Background(), "collectoroutput", root)
	require.NoError(t, err)
	require.NotEmpty(t, pop.Nodes, "corpus produced no nodes")

	// The ORDER IS THE CONTRACT, so it is borrowed rather than re-derived:
	// contribhash.SortFileGroupRows folds nodes by Id and edges by the seven-part
	// lessEdgeKey order the production hash uses. Its own doc block records why an
	// instrument that re-implements the order it measures cannot detect a change
	// in it.
	nodes, edges := contribhash.SortFileGroupRows(contribhash.FileGroup{
		Nodes: pop.Nodes,
		Edges: ToBatchEdges(pop.Edges, ""),
	})

	langs := map[string]struct{}{}
	files := map[string]struct{}{}

	var b strings.Builder
	for _, n := range nodes {
		if l := n.GetLanguage(); l != "" {
			langs[l] = struct{}{}
		}
		if f := n.GetFilePath(); f != "" {
			files[f] = struct{}{}
		}
		b.WriteString(strings.Join([]string{
			"node",
			n.GetId(),
			n.GetType(),
			n.GetSymbolName(),
			n.GetFilePath(),
			n.GetLanguage(),
			strconv.Itoa(int(n.GetStartLine())),
			strconv.Itoa(int(n.GetEndLine())),
			n.GetContent(),
			n.GetSignature(),
			strconv.FormatBool(n.GetIsExported()),
			strconv.FormatBool(n.GetIsTest()),
			n.GetTestKind(),
			n.GetDescription(),
			n.GetSource(),
			n.GetStatus(),
			n.GetSummary(),
			n.GetKeywords(),
		}, "\x1f"))
		md := n.GetMetadata()
		keys := make([]string, 0, len(md))
		for k := range md {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("\x1f" + k + "=" + md[k])
		}
		b.WriteString("\n")
	}
	for _, e := range edges {
		b.WriteString(strings.Join([]string{
			"edge",
			e.FromID,
			e.ToID,
			string(e.Type),
			strconv.FormatFloat(e.Weight, 'g', -1, 64),
			strconv.FormatFloat(e.Confidence, 'g', -1, 64),
			e.Method,
			e.Evidence,
		}, "\x1f"))
		b.WriteString("\n")
	}

	dirs, err := os.ReadDir(root)
	require.NoError(t, err)
	onDisk := map[string]struct{}{}
	for _, d := range dirs {
		if d.IsDir() {
			onDisk[d.Name()] = struct{}{}
		}
	}
	// COMPARED AS SETS, NOT AS COUNTS. Two counts agree whenever a language is
	// dropped and an unexpected one appears in the same pass — the two errors
	// cancel and the cross-check reads clean while covering the wrong corpus.
	// Set equality has no such blind spot, and it names WHICH language moved.
	missing, unexpected := setDiff(onDisk, langs), setDiff(langs, onDisk)
	if len(missing) > 0 || len(unexpected) > 0 {
		t.Fatalf("coverage cross-check failed against %s.\nThe digest below would pin a different "+
			"corpus than the one on disk.\nlanguage directories with NO emitted nodes: [%s]\n"+
			"emitted languages with NO directory: [%s]",
			collectorOutputCorpusRoot, strings.Join(missing, ","), strings.Join(unexpected, ","))
	}

	t.Logf("collector output identity: languages=%d files=%d nodes=%d edges=%d",
		len(langs), len(files), len(nodes), len(edges))

	sum := sha256.Sum256([]byte(b.String()))
	got := hex.EncodeToString(sum[:])
	if got != pinnedCollectorOutputDigest {
		t.Fatalf("collector output identity digest = %s, pinned = %s.\n"+
			"WHAT THE COLLECTOR EMITS CHANGED. If the change touched node Id, Summary, "+
			"Keywords or metadata, every per-file contribution hash is UNMOVED, so under "+
			"diff mode the affected files read UNCHANGED and never re-upload — the change "+
			"reaches no existing graph. In the SAME change you MUST: (1) bump "+
			"CollectorOutputVersion in package parser (collector_output_version.go), which "+
			"routes through the collector-version fallback and costs exactly one full "+
			"re-land per graph; and (2) update pinnedCollectorOutputDigest to %s.",
			got, pinnedCollectorOutputDigest, got)
	}
}
