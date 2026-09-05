// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// extract.go carries EXTRACT MODE's result shape and its two output ceilings.
//
// Extract mode answers one question: "show me what this recipe pulls out of this
// document". It writes nothing, and it returns rows — which is now the only
// thing a recipe run does.

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
// a recipe run writes nothing and extract rows carry no stable id, so the
// sentinel never reaches storage. It is confined to the in-run stable-id and
// emitted-set bookkeeping that the lookup and link rules compare within a single
// run.
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

// effectiveOffset resolves the row cursor once per run.
//
// A NEGATIVE OFFSET IS REFUSED RATHER THAN CLAMPED. It names no page a caller
// could have intended, and clamping would return a successful FIRST page for a
// request that was wrong — the caller then reads rows they did not ask for and
// nothing in the response says so. Zero is the ordinary unset value and means
// "start at the beginning", so it is not an error.
func effectiveOffset(opts Options) (int, error) {
	if opts.Offset < 0 {
		return 0, fmt.Errorf("recipe: extract offset must be zero or positive, got %d", opts.Offset)
	}
	return opts.Offset, nil
}

// recordExtractRow captures one emitted row, appending it only when the row is
// at or after the cursor AND the cap still allows, but counting EVERY matched
// row.
//
// The distinction is the whole point of honest disclosure: RowsMatched counts
// the same population the emitted nodes come from, so a caller reads "200 of
// 1543" instead of a silently short list that looks complete. Under the cursor
// that gains force rather than losing it — a row before the cursor is counted
// and dropped, so page three still reports the whole population behind it.
//
// THE INCREMENT COMES FIRST, BEFORE THE SKIP, and that order is the point.
// Decrementing the count back out, or skipping the increment for a pre-cursor
// row, compiles and satisfies every window assertion while destroying the one
// number that distinguishes a cursor overshoot from an empty match.
//
// The field map is handed over as computed — the emit path already builds a
// fresh one per row, so nothing is evaluated twice and nothing is copied.
func recordExtractRow(ex *ExtractResult, offset, cap int, nodeType, sourceNodeID string, fields map[string]string) {
	ex.RowsMatched++
	if ex.RowsMatched-1 < offset {
		return
	}
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
