// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"strconv"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	"github.com/fulminate-io/knowledge-mcp/internal/kgwire"
	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// The client-side MaterializeLogGraph owns graph construction;
// Pipeline.CollectFromEntries does not touch store.DB — see pipeline.go for the
// flow.

// AssembleGraphBatch converts templates, streams, and chunks into a single
// (nodes, edges) pair suitable for the client's PersistBatch wire path. Edges
// use FromID/ToID (FromIdx/ToIdx == -1) since every node already has a
// deterministic ID.
func AssembleGraphBatch(
	templates []*wirelogs.LogTemplate,
	streams []*wirelogs.LogStream,
	chunks []*wirelogs.LogChunk,
) ([]*knowledgev1.Node, []kgwire.BatchEdge) {
	nodes := make([]*knowledgev1.Node, 0, len(templates)+len(streams)+len(chunks))

	for _, tpl := range templates {
		nodes = append(nodes, templateNode(tpl))
	}
	for _, s := range streams {
		nodes = append(nodes, BuildStreamNode(s))
	}
	for _, c := range chunks {
		nodes = append(nodes, chunkNode(c))
	}

	// Shared label nodes — one per unique low-cardinality (key, value).
	labelNodes := BuildLabelNodes(streams)
	nodes = append(nodes, labelNodes...)

	labelIDs := buildLabelNodeIDMap(labelNodes)

	edges := make([]kgwire.BatchEdge, 0, len(chunks)*2+len(streams)*3)
	for _, s := range streams {
		labelEdges := BuildHasLabelEdges(s, labelIDs)
		for i := range labelEdges {
			edges = append(edges, BatchEdgeByID(labelEdges[i].FromId, labelEdges[i].ToId, kgtypes.EdgeType(labelEdges[i].Type)))
		}
	}
	for _, c := range chunks {
		// Chunk → Stream (belongs_to)
		edges = append(edges, BatchEdgeByID(c.ID, c.StreamID, kgtypes.EdgeBelongsTo))
		// Chunk → Template (instance_of via contains)
		edges = append(edges, BatchEdgeByID(c.TemplateID, c.ID, kgtypes.EdgeContains))
	}
	return nodes, edges
}

// templateNode serializes a wirelogs.LogTemplate to its graph node form. The
// node's SymbolName is set to the alias (when present) so BM25 search
// can match the readable form directly; the pattern lives in metadata
// so full-text and existing tooling still see it.
func templateNode(t *wirelogs.LogTemplate) *knowledgev1.Node {
	meta := map[string]string{
		"pattern":  t.Pattern,
		"severity": t.Severity,
		"count":    strconv.Itoa(t.Count),
	}
	if !t.FirstSeen.IsZero() {
		meta["first_seen"] = t.FirstSeen.UTC().Format(timestampMetaLayout)
	}
	if !t.LastSeen.IsZero() {
		meta["last_seen"] = t.LastSeen.UTC().Format(timestampMetaLayout)
	}
	alias := t.Alias
	if alias == "" {
		alias = TemplateAliasFor(t)
	}
	if alias != "" {
		meta["alias"] = alias
	}
	symbol := alias
	if symbol == "" {
		// Fall back to the pattern so legacy graphs without an alias
		// remain searchable.
		symbol = t.Pattern
	}
	return &knowledgev1.Node{
		Id:         t.ID,
		Type:       string(kgtypes.NodeLogTemplate),
		SymbolName: symbol,
		Metadata:   meta,
	}
}

// chunkNode serializes a wirelogs.LogChunk to its graph node form. The compressed
// payload is stored as Content (raw bytes-as-string); decompression is
// handled by DecodeChunk when readers need the underlying entries.
func chunkNode(c *wirelogs.LogChunk) *knowledgev1.Node {
	meta := map[string]string{
		"stream_id":   c.StreamID,
		"template_id": c.TemplateID,
		"entry_count": strconv.Itoa(c.EntryCount),
	}
	if !c.StartTime.IsZero() {
		meta["start_time"] = c.StartTime.UTC().Format(timestampMetaLayout)
	}
	if !c.EndTime.IsZero() {
		meta["end_time"] = c.EndTime.UTC().Format(timestampMetaLayout)
	}
	return &knowledgev1.Node{
		Id:       c.ID,
		Type:     string(kgtypes.NodeLogChunk),
		Content:  string(c.CompressedData),
		Metadata: meta,
	}
}

// buildLabelNodeIDMap maps "key=value" strings back to their node IDs.
// BuildLabelNodes stores the pair as SymbolName; the deterministic node
// ID is the LabelNodeID("key","value") form ("log-label:key=value"), so
// we can reconstruct the map directly from the nodes slice.
func buildLabelNodeIDMap(nodes []*knowledgev1.Node) map[string]string {
	m := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if kgtypes.NodeType(n.Type) != kgtypes.NodeLogLabel {
			continue
		}
		m[n.SymbolName] = n.Id
	}
	return m
}

// BatchEdgeByID constructs a BatchEdge pointing at existing node IDs
// (FromIdx/ToIdx==-1). Used when the node already lives in the graph
// (or is elsewhere in the same batch under a deterministic ID).
func BatchEdgeByID(from, to string, rel kgtypes.EdgeType) kgwire.BatchEdge {
	return kgwire.BatchEdge{
		FromIdx: -1,
		ToIdx:   -1,
		FromID:  from,
		ToID:    to,
		Type:    rel,
	}
}

// timestampMetaLayout is the RFC3339-nano format used to stringify
// timestamps in graph node metadata. Consistent formatting keeps the
// strings comparable across nodes and stable across process restarts.
const timestampMetaLayout = "2006-01-02T15:04:05.000000000Z07:00"
