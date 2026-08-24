// SPDX-License-Identifier: Apache-2.0

// Package remote implements collector.Sink backed by connect-go RPCs to the
// graph server. The client side of the split: collection runs in-process,
// chunks ride the unary IngestService CollectChunk + Finalize flow, server-side
// handlers own the carry-forward upsert + epoch GC.
package remote

import (
	"fmt"
	"strconv"

	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// DefaultBatchBytes caps the serialized size of a single CollectChunk's inline
// node payload at 4 MiB so each unary chunk carries a bite-sized frame rather
// than a multi-megabyte request. Client tuning knob; the server accepts any N
// chunks of any size identically (the wire is frozen — 1 chunk for a small repo,
// N for a large one, never a wire change).
const DefaultBatchBytes = 4 * 1024 * 1024

// BatchNodes GROUPS nodes by owning file and then packs whole files into inline
// []*Node chunks whose total serialized size stays under maxBytes. Each chunk is
// self-contained: it rides one CollectChunk request with the nodes INLINE (no
// by-hash arena indirection), so any server replica can land any chunk
// statelessly.
//
// THE INVARIANT IS: EVERY OWNING FILE APPEARS IN EXACTLY ONE CHUNK — the node-side
// twin of BatchEdgesProto's per-source invariant. The server's node reclaim is a
// per-chunk SET DIFFERENCE: it removes an uploaded file's resident
// collector-owned nodes that are absent from the chunk's incoming set for that
// file, which is only safe if the chunk it processes holds the file's COMPLETE
// node set. If a file were split across two chunks, the second would compute the
// first chunk's freshly-landed nodes as absent and delete them. Silently.
//
// GROUPING IS REQUIRED, NOT MERELY CUTTING AT BOUNDARIES, AND THAT IS A FACT ABOUT
// PRODUCTION ORDER. Node slices do not arrive grouped by file: the parser appends a
// FILE-LESS language node from inside the per-chunk loop (ensureLangNode), and the
// hierarchy append adds roughly 150 more file-less nodes at the tail, so a chunker
// that merely refused to cut mid-run would be a no-op against that input.
//
// FILE-LESS NODES ARE THEIR OWN GROUP, keyed by the empty path. They are outside
// the manifest and can never be declined, but they must not be split either: the
// grouping is what makes a chunk's file membership well-defined.
//
// CALLERS THAT HOLD A PARALLEL PER-NODE ARRAY MUST PERMUTE IT THEMSELVES, FIRST.
// The reordering here is a permutation of the input, so an index-aligned side
// array (the per-row contribution digests) would no longer line up with the
// chunks. WriteResult therefore calls groupNodesAndHashesByFile before this, which
// applies THIS SAME ORDER to both arrays — which also makes the grouping below the
// identity on the slice production actually hands it. The grouping is kept here
// regardless so the invariant is structural rather than a caller contract.
//
// A SINGLE FILE WHOSE NODES EXCEED maxBytes STILL LANDS IN ONE CHUNK, alone. The
// budget is a soft cap and the exactly-one-chunk invariant outranks it: splitting
// the largest file is precisely the case where a split would corrupt.
func BatchNodes(nodes []*knowledgev1.Node, maxBytes int) [][]*knowledgev1.Node {
	if maxBytes <= 0 {
		maxBytes = DefaultBatchBytes
	}
	order := fileGroupedOrder(nodes)

	var chunks [][]*knowledgev1.Node
	var cur []*knowledgev1.Node
	var curBytes int
	for i := 0; i < len(order); {
		// order is file-grouped, so one file's run is the maximal span of equal
		// paths starting at i — no second bucketing pass is needed to find it.
		path := nodes[order[i]].GetFilePath()
		j, groupBytes := i, 0
		for j < len(order) && nodes[order[j]].GetFilePath() == path {
			groupBytes += proto.Size(nodes[order[j]]) + 16 // rough proto field overhead per node
			j++
		}
		// Cut BEFORE the group when it will not fit, never inside it. An oversized
		// group lands whole in a chunk of its own, which this same branch produces:
		// cur is flushed, and the group becomes the new cur.
		if cur != nil && curBytes+groupBytes > maxBytes {
			chunks = append(chunks, cur)
			cur = nil
			curBytes = 0
		}
		for k := i; k < j; k++ {
			cur = append(cur, nodes[order[k]])
		}
		curBytes += groupBytes
		i = j
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// fileGroupedOrder returns the input indices reordered so that every owning
// file's nodes are CONTIGUOUS: files appear in order of first appearance, and
// within a file the collector's original node order is preserved. File-less nodes
// (empty file path) form one group under the empty key.
//
// IT RETURNS INDICES RATHER THAN NODES BECAUSE IT IS THE SINGLE GROUPING RULE FOR
// TWO CALLERS. BatchNodes applies it to the node slice; groupNodesAndHashesByFile
// applies the SAME order to the node slice AND to the index-aligned per-row digest
// array. Returning a permutation rather than bundles is what makes it impossible
// for those two sites to group differently — a second hand-rolled grouping is
// exactly how the node and hash arrays would drift apart.
//
// A BUCKET RATHER THAN A SORT, mirroring groupEdgesBySource: grouping is all the
// invariant needs, and bucketing is linear where a comparison sort is not. The
// result is a stable permutation of the input.
func fileGroupedOrder(nodes []*knowledgev1.Node) []int {
	order := make([]int, 0, len(nodes))
	byFile := make(map[string][]int, len(nodes))
	files := make([]string, 0, len(nodes))
	for i, n := range nodes {
		path := n.GetFilePath()
		if _, seen := byFile[path]; !seen {
			files = append(files, path)
		}
		byFile[path] = append(byFile[path], i)
	}
	for _, f := range files {
		order = append(order, byFile[f]...)
	}
	return order
}

// groupNodesAndHashesByFile permutes a node slice and its index-aligned per-row
// contribution digests TOGETHER into the file-grouped order BatchNodes packs in.
// It is the ONE place both arrays move, and it is why the chunk builder may keep
// slicing the digest array positionally.
//
// PERMUTING THE NODES ALONE IS THE SILENT FAILURE THIS EXISTS TO PREVENT. The
// digests are computed once over the whole node slice and sliced by POSITION as
// the chunks are built (collectChunkRequests walks `offset += len(nc)`), so a
// reorder applied to one array and not the other stores every node under a
// neighbour's digest. The server's structural guard is a LENGTH check, and a
// permutation passes it: every file's server-side aggregate would then disagree
// with the client's, no file would ever decline, and the incremental collect
// would quietly revert to re-landing the repository with every gate still green.
//
// A NIL DIGEST ARRAY IS THE ONE LEGITIMATE NON-MATCHING LENGTH, the same contract
// filterToChangedFiles keeps: a collect that carries no digests sends none, so
// there is nothing to keep aligned. ANY OTHER length disagreement is an error
// rather than a truncation — narrowing to the shorter array is precisely how a
// node would end up stored under a digest that was never its own.
func groupNodesAndHashesByFile(
	nodes []*knowledgev1.Node, hashes [][32]byte,
) ([]*knowledgev1.Node, [][32]byte, error) {
	if hashes != nil && len(hashes) != len(nodes) {
		return nil, nil, fmt.Errorf(
			"remote sink: file grouping: %d per-row node digests for %d nodes — the array is index-aligned "+
				"with the node slice by contract, so a differing length means the two came from different passes",
			len(hashes), len(nodes))
	}
	order := fileGroupedOrder(nodes)
	outNodes := make([]*knowledgev1.Node, len(nodes))
	var outHashes [][32]byte
	if hashes != nil {
		outHashes = make([][32]byte, len(nodes))
	}
	for pos, idx := range order {
		outNodes[pos] = nodes[idx]
		if outHashes != nil {
			outHashes[pos] = hashes[idx]
		}
	}
	return outNodes, outHashes, nil
}

// BatchEdgesProto GROUPS edges by source and then packs whole sources into
// []*BatchEdge chunks whose total serialized size stays under maxBytes. Each group
// rides one CollectChunk request so no single request body crosses the budget;
// maxBytes <= 0 defaults to kgwire.MaxCloudRequestBytes (the cloud request-body
// cap). Empty input → nil.
//
// THE INVARIANT IS: EVERY DISTINCT SOURCE APPEARS IN EXACTLY ONE CHUNK. The
// server's edge clear is a per-chunk SET DIFFERENCE — it deletes a source's
// resident collector-owned outbound edges that are absent from the chunk's
// incoming set for that source — and that is only safe if the chunk it processes
// holds the source's COMPLETE outbound set. If a source were split across two
// chunks, the second chunk would compute the first chunk's freshly-landed edges as
// absent and delete them. Corrupting, and silently so.
//
// GROUPING IS REQUIRED, NOT MERELY CUTTING AT BOUNDARIES, AND THAT IS A FACT ABOUT
// PRODUCTION ORDER. Edges do not arrive grouped by source and never have: the
// parser appends one language edge per declaration while walking every file's
// chunks, so the first region of the slice is every language edge in the
// repository, and every resolved call edge is appended after all of them. A single
// source's outbound set is therefore split across two regions tens of thousands of
// edges apart BY CONSTRUCTION, and a chunker that merely refused to cut mid-run
// would be a no-op against that input.
//
// A BUCKET RATHER THAN A SORT: grouping is all the invariant needs, and bucketing
// is linear where a comparison sort is not. Within a source the collector's
// original edge order is preserved, and sources are emitted in order of first
// appearance, so the output is a stable permutation of the input.
//
// A SINGLE SOURCE WHOSE EDGES EXCEED maxBytes STILL LANDS IN ONE CHUNK, alone. The
// budget is a soft cap and the exactly-one-chunk invariant outranks it: splitting
// the highest-fan-out source is precisely the case where a split would corrupt.
func BatchEdgesProto(edges []*knowledgev1.BatchEdge, maxBytes int) [][]*knowledgev1.BatchEdge {
	if maxBytes <= 0 {
		maxBytes = kgwire.MaxCloudRequestBytes
	}
	order, bySource := groupEdgesBySource(edges)

	var chunks [][]*knowledgev1.BatchEdge
	var cur []*knowledgev1.BatchEdge
	var curBytes int
	for _, src := range order {
		bundle := bySource[src]
		bundleBytes := 0
		for _, e := range bundle {
			bundleBytes += proto.Size(e) + 16 // rough proto field overhead per edge
		}
		// Cut BEFORE the bundle when it will not fit, never inside it. An
		// oversized bundle lands whole in a chunk of its own, which this same
		// branch produces: cur is flushed, and the bundle becomes the new cur.
		if cur != nil && curBytes+bundleBytes > maxBytes {
			chunks = append(chunks, cur)
			cur = nil
			curBytes = 0
		}
		cur = append(cur, bundle...)
		curBytes += bundleBytes
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// groupEdgesBySource buckets edges by their source key, returning the source keys
// in order of first appearance plus the per-source bundles in their original
// relative order. Splitting this out keeps BatchEdgesProto's packing loop readable
// and makes the grouping independently testable.
//
// THE KEY IS FromId, WITH FromIdx AS THE FALLBACK. Every edge on the collect path
// carries FromIdx = -1 and a resolved FromId (the converter that builds them sets
// both), so FromId is the real key. The index fallback exists so an edge that
// identifies its source positionally still groups with its own kind instead of
// collapsing every such edge onto one empty-string bucket.
func groupEdgesBySource(edges []*knowledgev1.BatchEdge) ([]string, map[string][]*knowledgev1.BatchEdge) {
	order := make([]string, 0, len(edges))
	bySource := make(map[string][]*knowledgev1.BatchEdge, len(edges))
	for _, e := range edges {
		key := e.GetFromId()
		if key == "" {
			key = "\x00idx:" + strconv.Itoa(int(e.GetFromIdx()))
		}
		if _, seen := bySource[key]; !seen {
			order = append(order, key)
		}
		bySource[key] = append(bySource[key], e)
	}
	return order, bySource
}
