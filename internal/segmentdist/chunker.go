// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
)

// BatchSegmentBlobs groups segment blobs into []*SegmentBlobProto sub-batches
// whose total serialized size stays under maxBytes, a structural sibling of
// remote.BatchNodes for the segment Ship path. Each sub-batch rides ONE
// ShipRequest so no single request body crosses the cloud cap even when the
// unshipped diff is large (the Cloudflare-fronted endpoint 413s oversize
// bodies). maxBytes <= 0 defaults to kgwire.MaxCloudRequestBytes. Blob order is
// preserved; a single oversized blob still gets its own sub-batch (the budget is
// a soft cap — a lone blob above the cap cannot be split further). Empty input → nil.
func BatchSegmentBlobs(blobs []*knowledgev1.SegmentBlobProto, maxBytes int) [][]*knowledgev1.SegmentBlobProto {
	if maxBytes <= 0 {
		maxBytes = kgwire.MaxCloudRequestBytes
	}
	var chunks [][]*knowledgev1.SegmentBlobProto
	var cur []*knowledgev1.SegmentBlobProto
	var curBytes int

	for _, b := range blobs {
		blobSize := proto.Size(b) + 16 // rough proto field overhead per blob
		if cur != nil && curBytes+blobSize > maxBytes {
			chunks = append(chunks, cur)
			cur = nil
			curBytes = 0
		}
		cur = append(cur, b)
		curBytes += blobSize
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}
