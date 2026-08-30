// SPDX-License-Identifier: Apache-2.0

package embed

import "context"

// Embedder interface declarations for the knowledge client's embedding backends.
// These co-locate with the arms that implement them — one file per arm in this
// package, each self-registering with the registry from its own init(). The OSS
// knowledge-server binary links no embedding code at all (by design — the
// server is a generic graph toolbox with zero LLM capability), so the interface
// contract lives client-side alongside the implementations.

// embedder is the common interface for all embedding backends.
type embedder interface {
	Available() bool
}

// binaryEmbedder generates binary (ubinary) embeddings as raw bytes.
// Implemented by every registered arm.
type binaryEmbedder interface {
	embedder
	EmbedBinary(ctx context.Context, text string) ([]byte, error)
	EmbedBinaryBatch(ctx context.Context, texts []string) ([][]byte, error)
}

// BinaryEmbedder is the exported alias of binaryEmbedder — NewEmbedder returns
// it and client importers (llmproviders, the tools maybeEmbedQuery path)
// type-qualify against it. It stays at exactly these three methods: the
// query/document distinction is a CONSTRUCTION parameter (Config.InputRole),
// not a per-call one, so widening the interface would force every arm and
// every test double to implement methods for a value that never varies
// within one instance.
type BinaryEmbedder = binaryEmbedder
