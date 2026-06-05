// SPDX-License-Identifier: Apache-2.0

package embed

import "context"

// Embedder interface declarations for the stdio client's embedding backends.
// These co-locate with their single concrete implementation (voyageEmbedder +
// NewVoyageBinaryEmbedder in voyage.go). The OSS knowledge-server binary links
// no embedding code at all (by design — the server is a generic graph toolbox
// with zero LLM capability), so the interface contract
// lives client-side alongside its only implementation.

// embedder is the common interface for all embedding backends.
type embedder interface {
	Available() bool
}

// binaryEmbedder generates binary (ubinary) embeddings as raw bytes.
// Implemented by voyageEmbedder.
type binaryEmbedder interface {
	embedder
	EmbedBinary(ctx context.Context, text string) ([]byte, error)
	EmbedBinaryBatch(ctx context.Context, texts []string) ([][]byte, error)
}

// BinaryEmbedder is the exported alias of binaryEmbedder — the NewVoyageBinaryEmbedder
// constructor returns it and client importers (llmproviders, the tools
// maybeEmbedQuery path) type-qualify against it.
type BinaryEmbedder = binaryEmbedder
