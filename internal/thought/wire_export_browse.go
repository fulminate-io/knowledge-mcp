// SPDX-License-Identifier: Apache-2.0

package thought

import (
	"context"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// DrainThoughtBrowse is the exported corpus-complete browse wrapper around
// drainThoughtBrowse for cmd/knowledge/internal/tools/ — the session-edge
// backfill needs to drain EVERY node of a type (NodeThought, then
// NodeThoughtSession) in bounded offset pages, defeating the engine's
// limit<=0 → browseDefaultLimit(10) cap, without re-exposing the unexported
// helper. Mirrors the FetchEdgesForNodeSet / FetchNodesByIDs export idiom
// (wire.go:256, wire.go:263). Pure delegation, no new logic.
func DrainThoughtBrowse(ctx context.Context, gc Caller, nodeType string, pageSize int) ([]*knowledgev1.Node, error) {
	return drainThoughtBrowse(ctx, gc, nodeType, pageSize)
}
