// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"fmt"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// truncation_notice.go holds the standard server-row-ceiling disclosure: the
// SINGLE declaration of the sentence, and the response-free wrapper the client
// intercept arms use. Split into a sibling file so dispatch.go stays under the
// 500-line cap — the same reason dispatch_byid.go documents for its own split.
// engine.WithTruncationNotice stays beside Render in dispatch.go because that is
// where the Render path reads.

// WithTruncationNoticeFor appends the standard truncation disclosure to an
// already-rendered result, returning res unchanged when truncated is false.
//
// This is the arms-without-a-response variant: it takes the verdict and the row
// count directly. The arms that need it — the examine and by-id edge summaries,
// and the analyze call-graph walks — receive their verdict out of a drain
// callback or a helper's return value and never hold the ExecuteResponse it came
// from, so demanding one would mean inventing a response to satisfy a signature.
// Arms that DO hold the response call engine.WithTruncationNotice instead, which
// is this function with the verdict and row count read off it.
//
// The notice is a SEPARATE content block, never concatenated into the existing
// text: the blocks are delivered as an array, so a format=json payload stays in
// its own block and remains independently parseable.
func WithTruncationNoticeFor(res kgtools.ToolResult, truncated bool, rows int) kgtools.ToolResult {
	if !truncated {
		return res
	}
	res.Content = append(res.Content, kgtools.ContentBlock{
		Type: "text",
		Text: truncationNoticeFor(rows),
	})
	return res
}

// truncationNoticeFor is the caller-facing sentence for a clamped result, and
// the SINGLE declaration of it: every arm reaches this text through
// WithTruncationNotice or WithTruncationNoticeFor, never by restating it. A
// second copy in an intercept is the failure this extraction exists to prevent,
// and a census test asserts the sentence keeps exactly one non-test declaration.
//
// Product copy: plain, actionable, and free of internal vocabulary — it says
// "the server row ceiling", never a constant name. It names `limit` verbatim so
// a reader maps the advice onto the actual parameter.
//
// plan_tree's withTruncationNotice (tools/intercept_query_plan_tree.go) carries a
// DIFFERENT action clause on purpose and is deliberately NOT folded in here: a
// tree has no pages to walk, and plan_tree's `limit` IS the subtree depth, so the
// re-run that yields a complete result is a smaller one.
func truncationNoticeFor(rows int) string {
	return fmt.Sprintf(
		"Showing %d rows — the server row ceiling engaged, so this result may be incomplete. "+
			"Re-run with an explicit `limit` and page until a short page for a complete set.",
		rows)
}
