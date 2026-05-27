// SPDX-License-Identifier: Apache-2.0

// Package remote implements collector.Sink backed by connect-go RPCs to the
// graph server. The client side of the split: collection runs in-process,
// chunks stream over IngestService, server-side handlers own staging +
// reindex + PROMOTE.
package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// DefaultBatchBytes caps the serialized size of a single ChunkBatch at 4
// MiB so the bi-di stream carries bite-sized frames rather than pausing on
// multi-megabyte uploads. Client tuning knob; server accepts any size.
const DefaultBatchBytes = 4 * 1024 * 1024

// hashNodeWithBody is an internal helper returning the hash and the body
// so BatchNodes can marshal once per node.
func hashNodeWithBody(n *knowledgev1.Node) (string, []byte, error) {
	body, err := json.Marshal(n)
	if err != nil {
		return "", nil, fmt.Errorf("remote: hashNode marshal: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), body, nil
}

// BatchNodes groups nodes into ChunkBatches whose total serialized body
// size stays under maxBytes. Each batch is self-contained: receiving one
// batch gives the server enough information to ack everything in it.
//
// Returns the produced batches plus a parallel slice of per-node hashes
// (index-aligned with the input nodes). WriteResult references the hash
// slice to tell the server which previously-uploaded chunks compose the
// CollectResult.
func BatchNodes(nodes []*knowledgev1.Node, maxBytes int) ([]*knowledgev1.ChunkBatch, []string, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultBatchBytes
	}
	hashes := make([]string, len(nodes))
	var batches []*knowledgev1.ChunkBatch
	var cur *knowledgev1.ChunkBatch
	var curBytes int

	for i, n := range nodes {
		h, body, err := hashNodeWithBody(n)
		if err != nil {
			return nil, nil, err
		}
		hashes[i] = h

		env := &knowledgev1.ChunkEnvelope{
			Hash:     h,
			NodeJson: body,
		}
		envSize := len(h) + len(body) + 16 // rough proto overhead per envelope

		if cur == nil || curBytes+envSize > maxBytes {
			cur = &knowledgev1.ChunkBatch{}
			batches = append(batches, cur)
			curBytes = 0
		}
		cur.Chunks = append(cur.Chunks, env)
		curBytes += envSize
	}
	return batches, hashes, nil
}
