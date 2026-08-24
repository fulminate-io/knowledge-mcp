// SPDX-License-Identifier: Apache-2.0

// Package contribhash computes the per-file contribution hashes the
// incremental-collect manifest is diffed against.
//
// THE SPEC IS docs/collect-contribution-hash.md AND IS NOT RESTATED HERE. This
// package implements its sections B (field encoding), C (node tuple), D (edge
// tuple), E (FromId ownership) and F (per-file aggregate).
//
// WHY A THIRD IMPLEMENTATION. The cloud flavor computes the same hash as a
// Postgres generated column and the OSS server as a hand-rolled Go twin; this is
// the third. cmd/knowledge and cmd/knowledge-server are separate modules and
// AGENTS.md denies any hand-written package shared between them — the wire
// contract is generated code only — so the client cannot import the server's.
// What keeps the three from drifting is not discipline but the wire: the manifest
// response carries hash_scheme_version and a mismatch forces a full collect.
package contribhash

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"runtime"
	"sort"
	"sync"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// ContributionHashSchemeVersion is the client's declaration of the scheme version
// (spec section A). It exists in TWO places by design — here and in the OSS server
// package — because no package may be shared across the module boundary. The cloud
// render is NOT a third declaration: cloud/store/manifest.go serves this same Go
// const. The manifest response carries the server's value; a mismatch forces one
// full collect, so drift is fail-closed by construction rather than by discipline.
//
// ANY CHANGE TO THE COLLECTOR'S DETERMINISTIC COMPOSITION SURFACE REQUIRES A BUMP
// HERE AND IN THE OTHER DECLARATION, in the same change — see spec section C for
// why the hash cannot see Summary/Keywords and what that costs.
//
// AT 2 SINCE THE EDGE-KEY CHANGE, which changed what an edge tuple CONTAINS in
// two ways at once: IMPORTS edges now carry a per-site group key where they carried none, and
// multi-candidate reference keys became position-independent. Both are the
// hashed-field-contents case of section A's calculus, and one bump covers both.
const ContributionHashSchemeVersion uint32 = 2

// appendAbsent appends spec section B's absent encoding: the single byte 0x00.
func appendAbsent(b []byte) []byte { return append(b, 0x00) }

// appendPresent appends spec section B's present encoding: 0x01, then the
// canonical bytes' length as a big-endian uint32, then the canonical bytes.
func appendPresent(b, canonical []byte) []byte {
	b = append(b, 0x01)
	b = binary.BigEndian.AppendUint32(b, uint32(len(canonical)))
	return append(b, canonical...)
}

// appendText encodes a TEXT field. THE GO ZERO VALUE IS ABSENT: the server's
// write path binds "" as SQL NULL, so a collector-owned row never holds an empty
// string and encoding "" as present-with-length-zero would disagree with what the
// server stores on every empty column.
func appendText(b []byte, v string) []byte {
	if v == "" {
		return appendAbsent(b)
	}
	return appendPresent(b, []byte(v))
}

// appendInt encodes an INT32 field as base-10 decimal ASCII, with a leading '-'
// when negative. Zero is absent (the server binds 0 as SQL NULL).
func appendInt(b []byte, v int32) []byte {
	if v == 0 {
		return appendAbsent(b)
	}
	var digits []byte
	digits = appendDecimal(digits, v)
	return appendPresent(b, digits)
}

// appendDecimal renders v as base-10 ASCII without an intermediate string.
func appendDecimal(dst []byte, v int32) []byte {
	if v < 0 {
		dst = append(dst, '-')
	}
	var buf [10]byte
	i := len(buf)
	u := uint32(v)
	if v < 0 {
		u = uint32(-v)
	}
	for {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
		if u == 0 {
			break
		}
	}
	return append(dst, buf[i:]...)
}

// appendBool encodes a BOOLEAN field as the single byte 't' or 'f'. FALSE IS
// ABSENT (the server binds false as SQL NULL), so only true reaches the present
// branch from this path; 'f' is spelled out because the encoding is defined over
// the column's value space rather than over what one writer happens to produce.
func appendBool(b []byte, v bool) []byte {
	if !v {
		return appendAbsent(b)
	}
	return appendPresent(b, []byte{'t'})
}

// appendFloat encodes a DOUBLE PRECISION field as the 8-byte IEEE-754 big-endian
// representation — byte-level rather than textual because Go's strconv 'g'/17 and
// Postgres's float8out do not agree on every value. Zero is absent.
func appendFloat(b []byte, v float64) []byte {
	if v == 0 {
		return appendAbsent(b)
	}
	var enc [8]byte
	binary.BigEndian.PutUint64(enc[:], math.Float64bits(v))
	return appendPresent(b, enc[:])
}

// NodeContributionHash returns the SHA-256 over the 14 encoded node fields of
// spec section C, in the order the server's skip guard compares them. Metadata,
// Summary and Keywords are excluded as server-added.
func NodeContributionHash(n *knowledgev1.Node) [32]byte {
	buf := make([]byte, 0, nodeBufHint(n))
	buf = appendText(buf, n.GetType())
	buf = appendText(buf, n.GetSymbolName())
	buf = appendText(buf, n.GetFilePath())
	buf = appendText(buf, n.GetLanguage())
	buf = appendInt(buf, n.GetStartLine())
	buf = appendInt(buf, n.GetEndLine())
	buf = appendText(buf, n.GetContent())
	buf = appendText(buf, n.GetSignature())
	buf = appendBool(buf, n.GetIsExported())
	buf = appendBool(buf, n.GetIsTest())
	buf = appendText(buf, n.GetTestKind())
	buf = appendText(buf, n.GetDescription())
	buf = appendText(buf, n.GetSource())
	buf = appendText(buf, n.GetStatus())
	return sha256.Sum256(buf)
}

// nodeBufHint sums the hashed text lengths plus a fixed framing allowance so the
// encoder sizes its buffer once instead of growing it.
func nodeBufHint(n *knowledgev1.Node) int {
	const framingPerField = 5
	const nodeFieldCount = 14
	return len(n.GetType()) + len(n.GetSymbolName()) + len(n.GetFilePath()) +
		len(n.GetLanguage()) + len(n.GetContent()) + len(n.GetSignature()) +
		len(n.GetTestKind()) + len(n.GetDescription()) + len(n.GetSource()) +
		len(n.GetStatus()) + nodeFieldCount*framingPerField + 32
}

// EdgeContributionHash returns the SHA-256 over the 7 encoded edge fields of spec
// section D. LastValidated is excluded because the collector never sets it — the
// only non-test reference under the collector decodes a server response rather
// than emitting an edge — and TombstonedAt because it is server state.
//
// Every one of the seven survives ToBatchEdges, which is why hashing the
// BatchEdge carrier loses nothing the *knowledgev1.Edge form carried.
func EdgeContributionHash(e kgwire.BatchEdge) [32]byte {
	const framingPerField = 5
	const edgeFieldCount = 7
	buf := make([]byte, 0, len(e.FromID)+len(e.ToID)+len(e.Type)+
		len(e.Method)+len(e.Evidence)+edgeFieldCount*framingPerField+16)
	buf = appendText(buf, e.FromID)
	buf = appendText(buf, e.ToID)
	buf = appendText(buf, string(e.Type))
	buf = appendFloat(buf, e.Weight)
	buf = appendFloat(buf, e.Confidence)
	buf = appendText(buf, e.Method)
	buf = appendText(buf, e.Evidence)
	return sha256.Sum256(buf)
}

// FileContributionHash returns the per-file hash of spec section F from the
// already-ordered per-row hashes: sha256(node_agg || edge_agg), where node_agg is
// the SHA-256 over the concatenated node hashes and edge_agg the SHA-256 over the
// concatenated edge hashes.
//
// THE CALLER OWNS THE ORDER, and the orders are the spec's: nodes by Id, and
// edges by (FromId, ToId, Type, Evidence) with the client additionally breaking
// remaining ties on weight, confidence and method — see lessEdgeKey for why
// Evidence is the fourth term rather than the last, and why the triple alone was
// a defect. Both are BYTE-WISE, which is what the cloud render's COLLATE "C"
// spells, Go's string comparison already being byte-wise.
//
// A file with no owned edges hashes the empty byte string for its edge aggregate,
// matching the render's CASE arm for a missing edge_agg.
func FileContributionHash(nodeHashes, edgeHashes [][32]byte) [32]byte {
	nodeAgg := sha256.New()
	for i := range nodeHashes {
		nodeAgg.Write(nodeHashes[i][:])
	}
	edgeAgg := sha256.New()
	for i := range edgeHashes {
		edgeAgg.Write(edgeHashes[i][:])
	}
	file := sha256.New()
	file.Write(nodeAgg.Sum(nil))
	file.Write(edgeAgg.Sum(nil))
	var out [32]byte
	copy(out[:], file.Sum(nil))
	return out
}

// maxHashWorkers caps the per-file hashing fan-out, following the chunker's own
// idiom (parser.maxChunkWorkers): each worker keeps one file's node and edge
// slices live while it hashes, so an uncapped NumCPU fan-out multiplies in-flight
// memory for little gain.
const maxHashWorkers = 8

// RowContributionHashes returns the PER-ROW contribution digests for a whole
// collect result — one per node and one per edge, in INPUT ORDER — by calling
// the same NodeContributionHash and EdgeContributionHash encoders above. There
// is no second algorithm here; the only new thing is the ordered fan-out.
//
// IT IS THE PER-ROW ENTRY POINT AND FileContributionHashes IS THE PER-FILE ONE,
// and they are reached from DIFFERENT places on purpose. contribution_hash is a
// column on EVERY graph's node and edge tables, so a web or pdf collect that
// sent none would leave the column NULL — which is why the caller invokes this
// outside the diff-eligibility gate, while the per-file map stays inside it.
//
// ORDER IS THE CONTRACT. The caller pairs these digests with its own slices by
// INDEX, all the way onto the wire's index-aligned node_contribution_hashes
// array, so a returned slice is only meaningful alongside the exact slice it was
// computed from. Each worker writes out[i] for its own i, so the ordering needs
// no mutex.
func RowContributionHashes(nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) (nodeHashes, edgeHashes [][32]byte) {
	nodeHashes = make([][32]byte, len(nodes))
	edgeHashes = make([][32]byte, len(edges))
	total := len(nodes) + len(edges)
	if total == 0 {
		return nodeHashes, edgeHashes
	}
	// hashAt indexes the two slices as one space so a single fan-out covers both
	// without a second worker pool: [0, len(nodes)) are nodes, the rest edges.
	hashAt := func(i int) {
		if i < len(nodes) {
			nodeHashes[i] = NodeContributionHash(nodes[i])
			return
		}
		j := i - len(nodes)
		edgeHashes[j] = EdgeContributionHash(edges[j])
	}

	workers := min(runtime.NumCPU(), total, maxHashWorkers)
	if workers <= 1 {
		for i := range total {
			hashAt(i)
		}
		return nodeHashes, edgeHashes
	}

	// The same bounded fan-out FileContributionHashes uses, over indices rather
	// than paths. Per-item rather than per-range because hashed row sizes vary by
	// orders of magnitude — a file's whole body rides Content — so contiguous
	// ranges would leave workers idle behind one large partition.
	var wg sync.WaitGroup
	idxCh := make(chan int, workers)
	for range workers {
		wg.Go(func() {
			for i := range idxCh {
				hashAt(i)
			}
		})
	}
	for i := range total {
		idxCh <- i
	}
	close(idxCh)
	wg.Wait()
	return nodeHashes, edgeHashes
}

// FileContributionHashes is the WHOLE-RESULT entry point and the only one the
// rest of the collect path calls. It partitions the result by owning file
// (PartitionByOwningFile, spec section E) and returns one hash per owning file.
//
// THE FILELESS GROUP HAS NO ENTRY. A node with no file path is outside the
// manifest (spec sections G and H), so its group is never a key here — which is
// what makes the returned key set equal to the client's set of files this collect
// positively handled: a file that failed to read or parse contributes no node and
// therefore is not a key.
// FilelessContributionHash is the WHOLE-RESULT digest of the group
// FileContributionHashes has no entry for: the nodes belonging to no file — the
// hierarchy package nodes, the repo root, the language hub — and their edges.
//
// IT EXISTS BECAUSE THE FILELESS SET HAS NO OTHER DECLINE BASIS. Those nodes are
// outside the manifest by construction, so nothing ever marks them changed and a
// diff-mode collect re-uploads the whole set on every run. A digest over the set
// gives the client something to compare against.
//
// IT FOLDS THROUGH fileGroupHash, the SAME folder FileContributionHashes calls,
// deliberately: that folder is the basis of the server's per-file decline, so its
// determinism across collects is already proven by a shipped mechanism. A bespoke
// digest would have to re-establish that property from scratch.
//
// THE POPULATION IS A DELIBERATE SUPERSET OF THE DECLINED PAYLOAD.
// PartitionByOwningFile puts an edge in the fileless group when its FromID is
// UNKNOWN as well as when it resolves to a fileless node, while the upload filter
// drops an unknown-FromID edge in every case. So this covers a few edges the
// payload never carries. The direction is the safe one — an unknown-source edge
// changing forces an upload that was not strictly needed, never a changed payload
// going unsent. Do NOT "fix" it with a second predicate: a second spelling of the
// fileless population is the drift this reuse avoids.
func FilelessContributionHash(nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) [32]byte {
	_, fileless := PartitionByOwningFile(nodes, edges)
	return fileGroupHash(fileless)
}

func FileContributionHashes(nodes []*knowledgev1.Node, edges []kgwire.BatchEdge) map[string][32]byte {
	byFile, _ := PartitionByOwningFile(nodes, edges)
	paths := make([]string, 0, len(byFile))
	for path := range byFile {
		paths = append(paths, path)
	}

	out := make(map[string][32]byte, len(paths))
	workers := min(runtime.NumCPU(), len(paths), maxHashWorkers)
	if workers <= 1 {
		for _, path := range paths {
			out[path] = fileGroupHash(byFile[path])
		}
		return out
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	pathCh := make(chan string, workers)
	for range workers {
		wg.Go(func() {
			for path := range pathCh {
				h := fileGroupHash(byFile[path])
				mu.Lock()
				out[path] = h
				mu.Unlock()
			}
		})
	}
	for _, path := range paths {
		pathCh <- path
	}
	close(pathCh)
	wg.Wait()
	return out
}

// SortFileGroupRows returns COPIES of one file group's rows in the exact order
// section F folds them: nodes by Id, edges by lessEdgeKey's total order. The
// inputs are not mutated.
//
// IT IS EXPORTED FOR THE DIAGNOSTIC THAT NAMES A FILE'S HASH INPUTS, and that is
// worth an exported symbol because the alternative already failed: the bench diag
// re-derived the edge order INLINE as a three-part sort, so once the production
// order became the seven-part lessEdgeKey the instrument was listing rows in an
// order nobody folds — describing a hash nobody computes, which is precisely what
// its own doc block promised it would never do. An instrument that re-implements
// the thing it measures cannot detect a change in it.
func SortFileGroupRows(g FileGroup) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	nodes := make([]*knowledgev1.Node, len(g.Nodes))
	copy(nodes, g.Nodes)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].GetId() < nodes[j].GetId() })

	edges := make([]kgwire.BatchEdge, len(g.Edges))
	copy(edges, g.Edges)
	sort.Slice(edges, func(i, j int) bool { return lessEdgeKey(edges[i], edges[j]) })
	return nodes, edges
}

// fileGroupHash orders one file's rows per spec section F and folds them into the
// file hash.
func fileGroupHash(g FileGroup) [32]byte {
	nodes, edges := SortFileGroupRows(g)
	nodeHashes := make([][32]byte, len(nodes))
	for i, n := range nodes {
		nodeHashes[i] = NodeContributionHash(n)
	}
	edgeHashes := make([][32]byte, len(edges))
	for i := range edges {
		edgeHashes[i] = EdgeContributionHash(edges[i])
	}
	return FileContributionHash(nodeHashes, edgeHashes)
}

// lessEdgeKey orders edges TOTALLY, over every field the edge hash covers, and
// the totality is the point rather than a refinement.
//
// WHY THE TRIPLE ALONE WAS A DEFECT. (FromID, ToID, Type) stopped being unique
// when the edge identity gained the group key: two reference sites in one
// declaration resolving to the same candidate emit one edge EACH, sharing the
// triple and differing in Evidence. sort.Slice is not stable, so those rows had
// no determined order, and fileGroupHash folds the per-row hashes IN ORDER —
// so the file hash moved between two collects of an unchanged tree. Measured on
// this repository: 211 of 4,450 files own such a key, and across two adjacent
// K=0 collects 51 of 145 commonly-uploaded files changed hash with byte-identical
// edge multisets, re-uploading unchanged files on every collect.
//
// EVIDENCE IS THE FOURTH TERM, AND THAT POSITION IS LOAD-BEARING RATHER THAN
// ALPHABETICAL. The server's per-file aggregate orders by
// (from_id, to_id, type, COALESCE(evidence,”)) under COLLATE "C"
// (contributionManifestSQL), which is TOTAL over stored rows because those four
// are the edges table's unique identity. The two sides must agree on the
// DISCRIMINATING PREFIX or their file hashes differ for every file carrying a
// duplicated triple — so Evidence is compared here before Weight, Confidence and
// Method, matching the server, even though spec section D lists it last in the
// hashed TUPLE. Ordering by the tuple's own sequence would put Weight ahead of
// Evidence and disagree with the server whenever two rows differ in both.
//
// The trailing three terms cannot change any comparison the server also makes —
// the server never stores two rows sharing all four — and are present so this
// order is total over the CLIENT's set, which the diff computes before any
// server-side dedup has run.
//
// THIS CHANGE CAUSES NO BULK RE-UPLOAD, and that was measured rather than
// assumed. The file aggregate is COMPUTED at manifest-render time on both sides
// rather than stored, so once both carry the same total order they agree on the
// first collect after the change: the K=0 upload set fell from 161/154/160 files
// to 2, instead of the up-to-211 one-time re-land a stored-digest change would
// have forced. The two that remain are NOT this defect — their client hash is
// byte-stable across runs, so they are a client/server disagreement about the
// row SET rather than its order.
func lessEdgeKey(a, b kgwire.BatchEdge) bool {
	if a.FromID != b.FromID {
		return a.FromID < b.FromID
	}
	if a.ToID != b.ToID {
		return a.ToID < b.ToID
	}
	if a.Type != b.Type {
		return a.Type < b.Type
	}
	if a.Evidence != b.Evidence {
		return a.Evidence < b.Evidence
	}
	if a.Weight != b.Weight {
		return a.Weight < b.Weight
	}
	if a.Confidence != b.Confidence {
		return a.Confidence < b.Confidence
	}
	return a.Method < b.Method
}
