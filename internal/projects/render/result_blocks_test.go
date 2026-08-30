// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// Block-level readers for an assemble result. They live apart from
// resultTextRender in testutil_test.go, which CONCATENATES every block into one
// string: that flattening is fine for a text arm, but it makes a rider appended
// as its own block indistinguishable from one spliced into the payload, and it
// makes a json payload unparseable the moment anything trails it. Assertions
// about block structure — and every json-payload read — go through these.

// blocksOf runs Handle and returns the raw content blocks.
func blocksOf(t *testing.T, gc GraphCaller, args map[string]any) []kgtools.ContentBlock {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return Handle(context.Background(), gc, raw).Content
}

// assembleJSONPayload returns the json arm's payload block alone — the block
// before the trailing rendered-size disclosure. It is the block the format=json
// contract promises stays independently parseable.
func assembleJSONPayload(t *testing.T, f *graphFixture, args map[string]any) string {
	t.Helper()
	blocks := blocksOf(t, f.gc(), args)
	require.GreaterOrEqual(t, len(blocks), 2,
		"a json result is at least a payload block plus the size disclosure; got %d", len(blocks))
	return blocks[len(blocks)-2].Text
}
