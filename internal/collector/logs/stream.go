// SPDX-License-Identifier: Apache-2.0

package logs

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
	wirelogs "github.com/fulminate-io/knowledge-mcp/internal/logwire"
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// FingerprintLabels produces a deterministic hex hash from a label map.
// Keys are sorted, joined as "key=value" lines, then SHA-256 hashed.
func FingerprintLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return fmt.Sprintf("%x", sha256.Sum256(nil))
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte('\n')
	}
	h := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", h)
}

// NewLogStream creates a wirelogs.LogStream from a full label set. The tracker
// classifies labels into low-cardinality (shared graph nodes) and
// high-cardinality (stored inline). The stream ID is a fingerprint of
// all labels; the Fingerprint field uses only low-cardinality labels
// so streams sharing the same low-card labels have the same fingerprint.
func NewLogStream(labels map[string]string, tracker *CardinalityTracker) *wirelogs.LogStream {
	lowCard, highCard := tracker.Classify(labels)
	s := &wirelogs.LogStream{
		ID:             FingerprintLabels(labels),
		Labels:         labels,
		LowCardLabels:  lowCard,
		HighCardLabels: highCard,
		Fingerprint:    FingerprintLabels(lowCard),
	}
	// Derive the alias once at construction so downstream readers don't
	// have to recompute. Collisions are resolved later by the engine
	// layer when the stream set is fully known.
	s.Alias = AliasFor(s)
	return s
}

// BuildLabelNodes creates shared graph nodes for all unique low-cardinality
// label key=value pairs across the given streams. Each node has a deterministic
// ID of the form "log-label:key=value".
func BuildLabelNodes(streams []*wirelogs.LogStream) []*knowledgev1.Node {
	seen := make(map[string]struct{})
	var nodes []*knowledgev1.Node
	for _, s := range streams {
		for k, v := range s.LowCardLabels {
			id := LabelNodeID(k, v)
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			nodes = append(nodes, &knowledgev1.Node{
				Id:         id,
				Type:       string(kgtypes.NodeLogLabel),
				SymbolName: k + "=" + v,
				Metadata: map[string]string{
					"label_key":   k,
					"label_value": v,
				},
			})
		}
	}
	return nodes
}

// BuildStreamNode creates a graph node representing a single wirelogs.LogStream.
// The node's SymbolName is set to the stream's alias (when present) so
// BM25 search can match the readable form directly. The full label set
// is encoded in metadata as "label:<key>" so streamFromNode can
// reconstruct it.
func BuildStreamNode(stream *wirelogs.LogStream) *knowledgev1.Node {
	meta := make(map[string]string, len(stream.Labels)+2)
	for k, v := range stream.Labels {
		meta["label:"+k] = v
	}
	meta["fingerprint"] = stream.Fingerprint
	alias := stream.Alias
	if alias == "" {
		alias = AliasFor(stream)
	}
	if alias != "" {
		meta["alias"] = alias
	}
	return &knowledgev1.Node{
		Id:         stream.ID,
		Type:       string(kgtypes.NodeLogStream),
		SymbolName: alias,
		Metadata:   meta,
	}
}

// BuildHasLabelEdges creates has_label edges from a stream node to its
// shared low-cardinality label nodes. The caller must supply LabelNodeIDs
// mapping "key=value" strings to node IDs (as produced by BuildLabelNodes).
func BuildHasLabelEdges(stream *wirelogs.LogStream, LabelNodeIDs map[string]string) []knowledgev1.Edge {
	edges := make([]knowledgev1.Edge, 0, len(stream.LowCardLabels))
	for k, v := range stream.LowCardLabels {
		nodeID, ok := LabelNodeIDs[k+"="+v]
		if !ok {
			continue
		}
		edges = append(edges, knowledgev1.Edge{
			FromId: stream.ID,
			ToId:   nodeID,
			Type:   string(kgtypes.EdgeHasLabel),
		})
	}
	return edges
}

// LabelNodeID returns a deterministic node ID for a label key=value pair.
// Exported so the client-side log materializer can reference the same
// log-label IDs the structural-pass nodes use.
func LabelNodeID(key, value string) string {
	return "log-label:" + key + "=" + value
}
