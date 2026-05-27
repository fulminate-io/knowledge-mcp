// SPDX-License-Identifier: Apache-2.0

// Package tools — node→struct rebuild helpers used by the lazy
// QueryEngine cold-start path.
//
// Split out from tools_logs_query.go so that file stays under the
// 300-line soft cap. The helpers here are pure data conversions: every
// graph node persisted by the logs pipeline has a deterministic shape
// and these functions reverse it. They are intentionally
// best-effort — one corrupt row produces a partial struct rather than
// killing the whole rebuild.
package tools

import (
	"strconv"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	logwire "github.com/fulminate-io/knowledge-mcp/internal/logwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// templateFromNode rebuilds a LogTemplate from its serialized graph
// node. Missing/malformed metadata fields fall back to zero-values
// rather than erroring.
func templateFromNode(n *knowledgev1.Node) *logwire.LogTemplate {
	count, _ := strconv.Atoi(kgtypes.Value(n, "count"))
	return &logwire.LogTemplate{
		ID:        n.Id,
		Pattern:   kgtypes.Value(n, "pattern"),
		Severity:  kgtypes.Value(n, "severity"),
		Count:     count,
		FirstSeen: parseMetaTime(kgtypes.Value(n, "first_seen")),
		LastSeen:  parseMetaTime(kgtypes.Value(n, "last_seen")),
		Alias:     kgtypes.Value(n, "alias"),
	}
}

// streamFromNode rebuilds a LogStream from its serialized graph node.
// All labels land in LowCardLabels because the original low/high split
// isn't encoded in metadata (see package docs in pipeline_graph.go).
func streamFromNode(n *knowledgev1.Node) *logwire.LogStream {
	labels := make(map[string]string)
	for k, v := range n.Metadata {
		if rest, ok := strings.CutPrefix(k, "label:"); ok {
			labels[rest] = v
		}
	}
	return &logwire.LogStream{
		ID:             n.Id,
		Labels:         labels,
		LowCardLabels:  labels,
		HighCardLabels: map[string]string{},
		Fingerprint:    kgtypes.Value(n, "fingerprint"),
		Alias:          kgtypes.Value(n, "alias"),
	}
}

// chunkFromNode rebuilds a LogChunk from its graph node. Content holds
// the zstd-compressed entry stream (raw bytes as string) so DecodeChunk
// can recover timestamps + variable values on demand.
func chunkFromNode(n *knowledgev1.Node) *logwire.LogChunk {
	count, _ := strconv.Atoi(kgtypes.Value(n, "entry_count"))
	return &logwire.LogChunk{
		ID:             n.Id,
		StreamID:       kgtypes.Value(n, "stream_id"),
		TemplateID:     kgtypes.Value(n, "template_id"),
		StartTime:      parseMetaTime(kgtypes.Value(n, "start_time")),
		EndTime:        parseMetaTime(kgtypes.Value(n, "end_time")),
		CompressedData: []byte(n.Content),
		EntryCount:     count,
	}
}

// parseMetaTime parses an RFC3339-nano timestamp from metadata. An
// empty or malformed value yields the zero Time — consistent with how
// pipeline_graph writes optional timestamps.
func parseMetaTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02T15:04:05.000000000Z07:00", s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// templatesAsWire / streamsAsWire / chunksAsWire are the bulk-fetch
// counterparts to templateFromNode / streamFromNode / chunkFromNode.
// They convert a slice of *knowledgev1.Node values returned from the wire
// fetch into the logwire struct slices the QueryEngine expects.
func templatesAsWire(nodes []*knowledgev1.Node) []*logwire.LogTemplate {
	out := make([]*logwire.LogTemplate, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, templateFromNode(n))
	}
	return out
}

func streamsAsWire(nodes []*knowledgev1.Node) []*logwire.LogStream {
	out := make([]*logwire.LogStream, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, streamFromNode(n))
	}
	return out
}

func chunksAsWire(nodes []*knowledgev1.Node) []*logwire.LogChunk {
	out := make([]*logwire.LogChunk, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, chunkFromNode(n))
	}
	return out
}
