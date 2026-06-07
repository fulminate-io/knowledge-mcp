// SPDX-License-Identifier: Apache-2.0

// Package externalcollector runs a user-registered external collector binary
// and converts its JSON stdout into the in-tree collector wire payload.
//
// The package is client-internal: it lives under cmd/knowledge/internal so it
// never crosses the client/server module boundary, and the envelope it parses
// is a PLAIN-Go shape (Result/Node/Edge below) — NOT the generated proto
// knowledgev1.Node. The only contract that crosses to the server is the
// existing CollectChunk/UploadSink proto wire, which ToCollectResult feeds via
// collectorwire.CollectResult. There is no new shared package and no new proto.
package externalcollector

// Result is the JSON envelope an external collector binary emits on stdout. It
// is the plain-Go counterpart of collectorwire.CollectResult: a registered
// binary prints one Result as a single JSON object, the client parses it with
// encoding/json, and ToCollectResult converts it into the in-tree wire payload.
//
// This is deliberately NOT collectorwire.CollectResult: that type's Nodes field
// is []*knowledgev1.Node, a generated proto message carrying
// protoimpl.MessageState / unknownFields / sizeCache and a proto-keyed metadata
// map. Raw encoding/json into a proto message is brittle (the proto convention
// is protojson, not encoding/json). The settled design has the binary emit a
// plain JSON shape with arbitrary domain fields living in each Node's metadata
// map, so the envelope is a hand-written struct with explicit json tags.
type Result struct {
	GraphType string `json:"graph_type"`
	GraphName string `json:"graph_name"`
	Nodes     []Node `json:"nodes"`
	Edges     []Edge `json:"edges"`
}

// Node is one node emitted by the external binary. The fields mirror the
// writable subset of knowledgev1.Node (gen/knowledge/v1/engine.pb.go) — the
// fields a collector legitimately sets. Server-owned bookkeeping fields
// (CreatedAt, UpdatedAt, TombstonedAt, CollectEpoch) are deliberately omitted:
// the collect-write path stamps them, so an external binary cannot set them.
//
// Domain-specific data the binary wants to attach beyond these typed fields
// rides in Metadata (a free-form string→string map), exactly as the built-in
// collectors do.
type Node struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	SymbolName  string            `json:"symbol_name,omitempty"`
	FilePath    string            `json:"file_path,omitempty"`
	Language    string            `json:"language,omitempty"`
	StartLine   int               `json:"start_line,omitempty"`
	EndLine     int               `json:"end_line,omitempty"`
	Content     string            `json:"content,omitempty"`
	Signature   string            `json:"signature,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	Description string            `json:"description,omitempty"`
	Source      string            `json:"source,omitempty"`
	Status      string            `json:"status,omitempty"`
	Keywords    string            `json:"keywords,omitempty"`
	IsExported  bool              `json:"is_exported,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Edge is one edge emitted by the external binary. The fields mirror the
// wire-settable subset of kgwire.BatchEdge. The binary references endpoints by
// node ID (FromID/ToID); the index-based form (FromIdx/ToIdx) used internally
// by the chunker is not part of the external contract, so ToCollectResult sets
// both indices to -1 (the "use the ID" sentinel) for every converted edge.
type Edge struct {
	FromID     string  `json:"from_id"`
	ToID       string  `json:"to_id"`
	Type       string  `json:"type"`
	Weight     float64 `json:"weight,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Method     string  `json:"method,omitempty"`
	Evidence   string  `json:"evidence,omitempty"`
}
