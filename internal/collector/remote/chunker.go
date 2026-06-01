// SPDX-License-Identifier: Apache-2.0

// Package remote implements collector.Sink backed by connect-go RPCs to the
// graph server. The client side of the split: collection runs in-process,
// chunks ride the unary IngestService CollectChunk + Finalize flow, server-side
// handlers own the carry-forward upsert + epoch GC.
package remote

import (
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// DefaultBatchBytes caps the serialized size of a single CollectChunk's inline
// node payload at 4 MiB so each unary chunk carries a bite-sized frame rather
// than a multi-megabyte request. Client tuning knob; the server accepts any N
// chunks of any size identically (the wire is frozen — 1 chunk for a small repo,
// N for a large one, never a wire change).
const DefaultBatchBytes = 4 * 1024 * 1024

// BatchNodes groups nodes into inline []*Node chunks whose total serialized
// size stays under maxBytes. Each chunk is self-contained: it rides one
// CollectChunk request with the nodes INLINE (no by-hash arena indirection), so
// any server replica can land any chunk statelessly. Node order is preserved;
// a single oversized node still gets its own chunk (the budget is a soft cap).
func BatchNodes(nodes []*knowledgev1.Node, maxBytes int) [][]*knowledgev1.Node {
	if maxBytes <= 0 {
		maxBytes = DefaultBatchBytes
	}
	var chunks [][]*knowledgev1.Node
	var cur []*knowledgev1.Node
	var curBytes int

	for _, n := range nodes {
		nodeSize := proto.Size(n) + 16 // rough proto field overhead per node
		if cur != nil && curBytes+nodeSize > maxBytes {
			chunks = append(chunks, cur)
			cur = nil
			curBytes = 0
		}
		cur = append(cur, n)
		curBytes += nodeSize
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}
