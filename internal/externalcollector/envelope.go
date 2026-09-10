// SPDX-License-Identifier: Apache-2.0

// Package externalcollector hosts a user-registered CUSTOM COLLECTOR: an MCP
// provider (stdio or unauthenticated http) carrying one tool the client calls to
// populate a graph. It dials the provider, completes the MCP handshake, verifies
// the named tool advertises input and output schemas satisfying the collector
// contract, calls the tool, and converts the structured result into the in-tree
// collector wire payload.
//
// The package is client-internal: it lives under cmd/knowledge/internal so it
// never crosses the client/server module boundary, and the envelope it parses
// is a PLAIN-Go shape (Result/Node/Edge below) — NOT the generated proto
// knowledgev1.Node. The only contract that crosses to the server is the
// existing CollectChunk/UploadSink proto wire, which ToCollectResult feeds via
// collectorwire.CollectResult. There is no new shared package and no new proto.
//
// THE GRAPH FAMILY AND INSTANCE ARE CLIENT-DERIVED, never provider-supplied: the
// registration name is the family and the collect id is the instance, so the
// envelope carries neither and a provider cannot write into a graph type it was
// not registered as.
package externalcollector

// Result is the structured result a custom collector's MCP tool returns. It is
// the plain-Go counterpart of collectorwire.CollectResult: the provider returns
// one Result as the tool call's structuredContent, the client decodes it with
// encoding/json, and ToCollectResult converts it into the in-tree wire payload.
//
// This is deliberately NOT collectorwire.CollectResult: that type's Nodes field
// is []*knowledgev1.Node, a generated proto message carrying
// protoimpl.MessageState / unknownFields / sizeCache and a proto-keyed metadata
// map. Raw encoding/json into a proto message is brittle (the proto convention
// is protojson, not encoding/json). The settled design has the provider emit a
// plain JSON shape with arbitrary domain fields living in each Node's metadata
// map, so the envelope is a hand-written struct with explicit json tags.
//
// The shape here is the Go counterpart of contract/collector_output.schema.json,
// which is the artifact a collector author reads and the schema a result is
// validated against. Changing one without the other is the drift the contract
// test pins.
type Result struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
	// WalkComplete is the COMPLETENESS ASSERTION the provider makes: its walk
	// enumerated the whole source. It rides through to
	// collectorwire.CollectResult.WalkComplete, which is the server's deletion
	// guard, so a provider asserting false disables the deletion phase exactly as
	// an incomplete code walk does. The contract schema REQUIRES the field, so a
	// provider cannot omit it and disable deletion by silence.
	WalkComplete bool `json:"walk_complete"`
}

// Node is one node emitted by the provider. The fields mirror the
// writable subset of knowledgev1.Node (gen/knowledge/v1/engine.pb.go) — the
// fields a collector legitimately sets. Server-owned bookkeeping fields
// (CreatedAt, UpdatedAt, TombstonedAt, CollectEpoch) are deliberately omitted:
// the collect-write path stamps them, so a provider cannot set them.
//
// Domain-specific data the provider wants to attach beyond these typed fields
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

// Edge is one edge emitted by the provider. The fields mirror the
// wire-settable subset of kgwire.BatchEdge. The provider references endpoints by
// node ID (FromID/ToID); the index-based form (FromIdx/ToIdx) used internally
// by the chunker is not part of the external contract, so ToCollectResult sets
// both indices to -1 (the "use the ID" sentinel) for every converted edge.
//
// AN EDGE IS EITHER IN-GRAPH OR CROSS-GRAPH, AND THE TWO GRAPH-FAMILY FIELDS
// ARE THE ONLY DISCRIMINATOR. Naming neither makes the edge an ordinary edge of
// the collector's own graph, which ToCollectResult converts. Naming one of them
// puts that endpoint in ANOTHER graph: the edge is NOT converted, and is
// resolved by the collector-side pass into the linkage graph instead — a
// different graph and a different shape. The two never mix, because an edge
// written natively across a graph boundary breaks per-graph save isolation,
// which is what the proxy convention exists to prevent.
//
// AN EDGE NAMING BOTH IS REFUSED BY THE COLLECT PASS rather than resolved
// twice: one resolution enumerates one family and locates one endpoint in it, so
// an edge foreign at both ends names a relationship this contract cannot express.
type Edge struct {
	FromID     string  `json:"from_id"`
	ToID       string  `json:"to_id"`
	Type       string  `json:"type"`
	Weight     float64 `json:"weight,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Method     string  `json:"method,omitempty"`
	Evidence   string  `json:"evidence,omitempty"`

	// SourceGraph is the GRAPH FAMILY the edge's FromID lives in — the mirror of
	// TargetGraph below, for an edge whose FOREIGN endpoint is the source rather
	// than the destination. It is a graph type ("code", or a registered custom
	// family such as "acme-aws"), never a graph instance: the client enumerates
	// that family's loaded graphs and locates the endpoint among them, so the
	// instance is DERIVED rather than asserted by the provider.
	//
	// EMPTY IS THE IN-GRAPH CASE, on the same terms as TargetGraph: it is
	// `omitempty`, so an edge that does not set it marshals exactly as it did,
	// and absent is indistinguishable from present-and-empty downstream.
	//
	// AT MOST ONE OF THE TWO IS SET. An edge naming both is refused by the
	// collect pass; see this type's doc comment for why one resolution reaches
	// one family.
	SourceGraph string `json:"source_graph,omitempty"`

	// TargetGraph is the GRAPH FAMILY the edge's ToID lives in — a graph type
	// ("logs", or a registered custom family such as "acme-aws"), never a graph
	// instance: the client enumerates that family's loaded graphs and locates the
	// endpoint among them, so the instance is DERIVED rather than asserted by the
	// provider.
	//
	// EMPTY IS THE IN-GRAPH CASE and is the only shape that existed before this
	// field. It is `omitempty` so an edge that does not set it marshals exactly as
	// it did, and absent is indistinguishable from present-and-empty downstream.
	//
	// A PROXY THE COLLECTOR WANTS IN ITS OWN GRAPH DOES NOT USE THIS FIELD. That
	// shape — a node typed "proxy" plus an ordinary edge to it — is expressible
	// through the plain contract and lands in the collector's own graph, exactly
	// as the built-in log materializer's does.
	TargetGraph string `json:"target_graph,omitempty"`
}
