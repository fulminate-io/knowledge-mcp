// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

// bm25FieldsFromProto maps a wire *knowledgev1.Bm25Fields into the documented
// searchengine.Document.Fields keys (FieldSymbolName / FieldSummary / FieldKeywords
// / FieldDescription / FieldContent), skipping empty values. This is the CLIENT-side
// store-data↔engine seam the locked contract names as docFromNode (document.go) —
// but sourced from the wire map the server composed at the gap-scan site, NOT from a
// client-hydrated Node. A nil message returns a nil map (the engine drops a
// zero-field Document defensively), and a message with only empty strings likewise
// returns nil so no empty Document is built.
// BuildBM25FieldsFromProto is the EXPORTED wrapper of bm25FieldsFromProto for the
// cross-package segment_rebuild driver (cmd/knowledge/internal/tools), which reads
// the same server-composed wire Bm25Fields off the segment_rebuild scan items and
// must map them into searchengine.Document.Fields IDENTICALLY to the embed
// writeback path — single-sourced here so the two callers never drift.
func BuildBM25FieldsFromProto(m *knowledgev1.Bm25Fields) map[string]string {
	return bm25FieldsFromProto(m)
}

func bm25FieldsFromProto(m *knowledgev1.Bm25Fields) map[string]string {
	if m == nil {
		return nil
	}
	fields := make(map[string]string, 5)
	if v := m.GetSymbolName(); v != "" {
		fields[searchengine.FieldSymbolName] = v
	}
	if v := m.GetSummary(); v != "" {
		fields[searchengine.FieldSummary] = v
	}
	if v := m.GetKeywords(); v != "" {
		fields[searchengine.FieldKeywords] = v
	}
	if v := m.GetDescription(); v != "" {
		fields[searchengine.FieldDescription] = v
	}
	if v := m.GetContent(); v != "" {
		fields[searchengine.FieldContent] = v
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}
