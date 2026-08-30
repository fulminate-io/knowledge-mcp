// SPDX-License-Identifier: Apache-2.0

package recipe

import "github.com/fulminate-io/knowledge-mcp/internal/kgtypes"

// extract.go carries EXTRACT MODE's result shape and its two output ceilings.
//
// Extract mode answers a different question from a saved recipe run: not "write
// these patterns into a practice graph" but "show me what this recipe would pull
// out of this document". It writes nothing, and it returns rows.

const (
	// DefaultExtractMaxRows bounds how many rows one extract returns.
	//
	// The intended use is a targeted section render, so two hundred rows bounds
	// a pathological whole-document extract about two orders of magnitude above
	// the intended slice while still fitting in one tool response.
	DefaultExtractMaxRows = 200

	// DefaultExtractMaxBytes bounds the rendered size of one extract — roughly
	// sixteen thousand tokens, a hard safety bound on a single response.
	DefaultExtractMaxBytes = 65536
)

// extractSentinelGraphType is the target graph type an INLINE recipe body runs
// under, because an inline body carries no target metadata to read one from.
//
// WHERE IT CAN AND CANNOT BE OBSERVED, stated so nobody looks for it in a graph:
// extract skips the result write entirely and extract rows carry no stable id,
// so the sentinel never reaches storage. It is confined to the in-run stable-id
// and emitted-set bookkeeping that the lookup and link rules compare within a
// single run.
//
// It is deliberately NOT a collector-owned type and NOT the transformers store,
// which means both of the run's target fences PASS on it rather than being
// bypassed — the fences stay armed for every saved-recipe run exactly as before.
const extractSentinelGraphType = kgtypes.GraphType("extract")

// ExtractRow is one row an extract run returns: the emitted node type, the
// source node the row was derived from, and the emit block's evaluated fields.
type ExtractRow struct {
	Type         string
	SourceNodeID string
	Fields       map[string]string
}

// ExtractResult is the caller-facing output of an extract run.
//
// THE TWO CAPS ARE APPLIED IN DIFFERENT PLACES, and this struct is honest about
// which fields are filled by whom. RowsMatched, RowsReturned and the max_rows
// value of TruncatedBy are INTERPRETER-populated. BytesReturned, and TruncatedBy
// when it reads max_bytes, are RENDERER-populated: only the renderer knows
// rendered sizes, so the recipe package cannot compute them.
//
// A Result that has not been through the renderer therefore carries honest
// row-cap fields and explicitly zero byte-cap fields — rather than fields that
// look computed and are not.
//
// TruncatedBy is exactly one of "", "max_rows" or "max_bytes".
type ExtractResult struct {
	Rows          []ExtractRow
	RowsMatched   int
	RowsReturned  int
	BytesReturned int
	Truncated     bool
	TruncatedBy   string
}

// effectiveMaxRows resolves the row cap once per run. A zero or negative
// supplied value selects the default rather than "no limit".
func effectiveMaxRows(opts Options) int {
	if opts.MaxRows > 0 {
		return opts.MaxRows
	}
	return DefaultExtractMaxRows
}

// recordExtractRow captures one emitted row, appending it only while the cap
// allows but counting EVERY matched row.
//
// The distinction is the whole point of honest disclosure: RowsMatched counts
// the same population the emitted nodes come from, so a caller reads "200 of
// 1543" instead of a silently short list that looks complete.
//
// The field map is handed over as computed — the emit path already builds a
// fresh one per row, so nothing is evaluated twice and nothing is copied.
func recordExtractRow(ex *ExtractResult, cap int, nodeType, sourceNodeID string, fields map[string]string) {
	ex.RowsMatched++
	if len(ex.Rows) >= cap {
		ex.Truncated = true
		ex.TruncatedBy = "max_rows"
		return
	}
	ex.Rows = append(ex.Rows, ExtractRow{
		Type:         nodeType,
		SourceNodeID: sourceNodeID,
		Fields:       fields,
	})
	ex.RowsReturned = len(ex.Rows)
}
