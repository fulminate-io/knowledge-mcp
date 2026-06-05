// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/enginetest"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// scriptedTypeFakeCaller answers the log node fetches over the Execute carrier
// seam: a type-browse keyed by NodeType (or a by-ids hydrate) returns
// the configured knowledgev1.Node set via the nodes_json carrier. When the plan sets
// content_b64=true (the chunk fetch), Node.Content is base64-encoded on the
// wire so DecodeNodesContentB64 reverses it client-side. Records every Execute
// for the RPC-count assertions.
type scriptedTypeFakeCaller struct {
	byType map[string][]*knowledgev1.Node // node sets keyed by NodeType string
	byIDs  []*knowledgev1.Node            // node set for an IDs-based query
	execs  []*knowledgev1.ExecuteRequest
	err    error
}

// Call satisfies the interface; the log fetches route through Execute.
func (f *scriptedTypeFakeCaller) Call(_ context.Context, _ string, _ json.RawMessage) (kgtools.ToolResult, error) {
	return kgtools.ToolResult{}, nil
}

func (f *scriptedTypeFakeCaller) Execute(_ context.Context, req *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error) {
	f.execs = append(f.execs, req)
	if f.err != nil {
		return nil, f.err
	}
	q := req.GetQuery()
	var nodes []*knowledgev1.Node
	if len(q.GetIds()) > 0 {
		nodes = f.byIDs
	} else {
		typ := q.GetSelection().GetNodeType()
		set, ok := f.byType[typ]
		if !ok {
			return nil, &wireFetchTestError{msg: "no canned response for type=" + typ}
		}
		nodes = set
	}
	// content_b64 carrier: base64-encode Content so the chunk's raw bytes survive
	// JSON (DecodeNodesContentB64 reverses it). Under the value-embed flip
	// knowledgev1.Node carries a noCopy, so deep-copy each node via proto.Merge before
	// mutating Content — a shallow copy of []*knowledgev1.Node shares the pointee.
	out := nodes
	if q.GetContentB64() {
		out = make([]*knowledgev1.Node, len(nodes))
		for i := range nodes {
			cp := &knowledgev1.Node{}
			proto.Merge(cp, nodes[i])
			if cp.Content != "" {
				cp.Content = base64.StdEncoding.EncodeToString([]byte(cp.Content))
			}
			out[i] = cp
		}
	}
	resp := enginetest.ResponseWithNodes(out...)
	resp.Total = int64(len(out))
	return resp, nil
}

type wireFetchTestError struct{ msg string }

func (e *wireFetchTestError) Error() string { return e.msg }

// TestFetchAllLogNodes_ParsesTemplatesStreamsChunks asserts that three canned
// type-browse Executes parse into the expected node slices, and that chunk
// Content round-trips through the content_b64 carrier back to raw bytes.
func TestFetchAllLogNodes_ParsesTemplatesStreamsChunks(t *testing.T) {
	raw := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x00, 0xFF, 0x10}

	gc := &scriptedTypeFakeCaller{
		byType: map[string][]*knowledgev1.Node{
			string(kgtypes.NodeLogTemplate): {
				{Id: "tpl-1", Type: string(kgtypes.NodeLogTemplate), SymbolName: "tpl one", Metadata: map[string]string{"pattern": "tpl one", "severity": "ERROR"}},
				{Id: "tpl-2", Type: string(kgtypes.NodeLogTemplate), SymbolName: "tpl two", Metadata: map[string]string{"pattern": "tpl two"}},
			},
			string(kgtypes.NodeLogStream): {
				{Id: "stream-1", Type: string(kgtypes.NodeLogStream), Metadata: map[string]string{"label:service": "api"}},
			},
			string(kgtypes.NodeLogChunk): {
				{Id: "chunk-1", Type: string(kgtypes.NodeLogChunk), Metadata: map[string]string{"template_id": "tpl-1"}, Content: string(raw)},
			},
		},
	}

	tpls, strs, chs, err := fetchAllLogNodes(context.Background(), gc, "q")
	require.NoError(t, err)
	assert.Len(t, gc.execs, 3, "exactly three Execute calls (one per type)")
	require.Len(t, tpls, 2)
	assert.Equal(t, "tpl-1", tpls[0].Id)
	assert.Equal(t, "ERROR", tpls[0].Metadata["severity"])
	require.Len(t, strs, 1)
	assert.Equal(t, "stream-1", strs[0].Id)
	require.Len(t, chs, 1)
	assert.Equal(t, raw, []byte(chs[0].Content), "chunk content_b64 must round-trip to raw bytes")
}

// TestFetchLogNodesByIDs_OneRoundTrip asserts the bulk-hydrate path makes a
// single Execute (no per-ID loop) and returns the hydrated nodes.
func TestFetchLogNodesByIDs_OneRoundTrip(t *testing.T) {
	gc := &scriptedTypeFakeCaller{byIDs: []*knowledgev1.Node{
		{Id: "tpl-x", Type: string(kgtypes.NodeLogTemplate), SymbolName: "x", Metadata: map[string]string{"pattern": "x", "severity": "WARN"}},
		{Id: "tpl-y", Type: string(kgtypes.NodeLogTemplate), SymbolName: "y", Metadata: map[string]string{"pattern": "y"}},
	}}

	out, err := fetchLogNodesByIDs(context.Background(), gc, "q", []string{"tpl-x", "tpl-y"}, false)
	require.NoError(t, err)
	assert.Len(t, gc.execs, 1, "exactly one Execute for the bulk hydrate")
	require.Len(t, out, 2)
	assert.Equal(t, "tpl-x", out[0].Id)
	assert.Equal(t, "WARN", out[0].Metadata["severity"])
}

// TestFetchLogNodesByIDs_EmptyShortCircuits asserts an empty ID list returns
// nil without issuing any Execute.
func TestFetchLogNodesByIDs_EmptyShortCircuits(t *testing.T) {
	gc := &scriptedTypeFakeCaller{}
	out, err := fetchLogNodesByIDs(context.Background(), gc, "q", nil, false)
	require.NoError(t, err)
	assert.Nil(t, out)
	assert.Empty(t, gc.execs, "no Execute should fire for empty IDs")
}

// TestFetchAllLogNodes_PropagatesRPCError asserts a server error on one of the
// three Executes surfaces with the typed error context.
func TestFetchAllLogNodes_PropagatesRPCError(t *testing.T) {
	gc := &scriptedTypeFakeCaller{
		byType: map[string][]*knowledgev1.Node{
			string(kgtypes.NodeLogTemplate): nil,
			// streams + chunks intentionally missing → fake returns an error
		},
	}
	_, _, _, err := fetchAllLogNodes(context.Background(), gc, "q")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "streams")
}
