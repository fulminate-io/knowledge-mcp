// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/collectorwire"
	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// sink_chunk_position_test.go — WHAT A FAILED CHUNK SAYS ABOUT THE CHUNKS BEFORE
// IT.
//
// A collect uploads its rows in several CollectChunk calls, each landing in its
// own server-side batch. When one of them is refused, the earlier ones have
// already landed and stand until the next collect — and the caller's only signal
// is the error this loop returns. The live confirmation found a refusal on chunk
// 2 of 2 reporting "Nothing was written" while chunk 1's two nodes were in the
// graph, so an operator was told a half-written graph did not exist.
//
// THIS IS THE END THAT KNOWS. The request carries no chunk index and no chunk
// total, so the server's refusal can only speak for the chunk in front of it; i
// and len(reqs) are in scope HERE, which makes this the only place the
// collect-scoped sentence can be true.

// positionChunk builds a minimal CollectChunk request for the position tests. The
// payload is irrelevant — what is under test is which chunk failed, not what was
// in it.
func positionChunk(id string) *knowledgev1.CollectChunkRequest {
	return &knowledgev1.CollectChunkRequest{
		Epoch: 1, GraphType: "acme", GraphName: "board",
		Nodes: []*knowledgev1.Node{{Id: id, Type: "ticket", SymbolName: id}},
	}
}

// collectResultForPosition is the collect these chunks belong to. uploadChunks
// reads it only for its log fields, so it carries the identity and nothing else.
func collectResultForPosition() *collectorwire.CollectResult {
	return &collectorwire.CollectResult{
		GraphType: kgtypes.GraphType("acme"), GraphName: "board", WalkComplete: true,
	}
}

// refuseChunk is the server-side refusal these rows drive: an InvalidArgument,
// which the upload path does not retry, so the loop reports the first failure.
func refuseChunk() error {
	return connect.NewError(connect.CodeInvalidArgument,
		errTestSentinel("ingest: CollectChunk: acme/board: nodes[0] carries an undeclared type. This chunk was not written"))
}

// TestUploadChunks_AFailureOnTheFIRSTChunkSaysNothingWasWritten is the position
// the old sentence was right about, kept as its own row so the fix cannot be a
// blanket rewording that makes every failure claim a partial write.
func TestUploadChunks_AFailureOnTheFIRSTChunkSaysNothingWasWritten(t *testing.T) {
	client, rec := startRecordingIngest(t)
	rec.chunkErr = refuseChunk()
	sink := NewUploadSink(client)

	err := sink.uploadChunks(context.Background(), "acme", collectResultForPosition(),
		[]*knowledgev1.CollectChunkRequest{positionChunk("T-1"), positionChunk("T-2")}, 1, 2, 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "CollectChunk 1/2", "the failing position is named, as it always was")
	assert.Contains(t, err.Error(), "no chunk of this collect was written",
		"a failure on the first chunk really did write nothing, and the caller is told so plainly")
	assert.NotContains(t, err.Error(), "earlier chunk",
		"there is no earlier chunk to stand: %s", err.Error())
}

// TestUploadChunks_AFailureOnALATERChunkNamesWhatAlreadyLanded is the live
// finding's own position. THE COUNT IS THE LOAD-BEARING PART: "this chunk
// failed" leaves the operator exactly where the false sentence did, with no
// reason to go looking for the rows that are in the graph.
func TestUploadChunks_AFailureOnALATERChunkNamesWhatAlreadyLanded(t *testing.T) {
	client, rec := startRecordingIngest(t)
	rec.chunkErr = refuseChunk()
	rec.chunkErrAfter = 1 // chunk 1 lands, chunk 2 is refused
	sink := NewUploadSink(client)

	err := sink.uploadChunks(context.Background(), "acme", collectResultForPosition(),
		[]*knowledgev1.CollectChunkRequest{positionChunk("T-1"), positionChunk("T-2")}, 1, 2, 0)

	require.Error(t, err)
	require.Equal(t, 1, chunkCount(rec),
		"the fixture must really have landed the first chunk, or the sentence under test would be asserting a state that did not happen")
	assert.Contains(t, err.Error(), "CollectChunk 2/2")
	assert.Contains(t, err.Error(), "1 earlier chunk of this collect already landed and stands until the next collect",
		"the caller is told how much of the collect is in the graph, which is what turns an error into something to act on")
	assert.NotContains(t, err.Error(), "no chunk of this collect was written",
		"and never the first-chunk sentence, which is the false claim this whole change is about: %s", err.Error())
}

// TestUploadChunks_TheEarlierChunkCountIsThePOSITION, not a fixed word: a failure
// on chunk 3 names two earlier chunks, so a hard-coded "1 earlier chunk" cannot
// pass. It also pins the PLURAL, which is the shape a fmt of the count alone
// gets wrong.
func TestUploadChunks_TheEarlierChunkCountIsThePOSITION(t *testing.T) {
	client, rec := startRecordingIngest(t)
	rec.chunkErr = refuseChunk()
	rec.chunkErrAfter = 2
	sink := NewUploadSink(client)

	err := sink.uploadChunks(context.Background(), "acme", collectResultForPosition(),
		[]*knowledgev1.CollectChunkRequest{positionChunk("T-1"), positionChunk("T-2"), positionChunk("T-3")}, 1, 3, 0)

	require.Error(t, err)
	require.Equal(t, 2, chunkCount(rec))
	assert.Contains(t, err.Error(), "CollectChunk 3/3")
	assert.Contains(t, err.Error(), "2 earlier chunks of this collect already landed and stand until the next collect",
		"the count and its plural follow the position: %s", err.Error())
}

// TestUploadChunks_TheServerSREFUSALSURVIVESTheAddedSentence is the control every
// row above needs: the position statement is APPENDED to the cause, never
// replaces it, so the operator still reads which type was refused and what to do
// about it.
func TestUploadChunks_TheServerSREFUSALSURVIVESTheAddedSentence(t *testing.T) {
	client, rec := startRecordingIngest(t)
	rec.chunkErr = refuseChunk()
	rec.chunkErrAfter = 1
	sink := NewUploadSink(client)

	err := sink.uploadChunks(context.Background(), "acme", collectResultForPosition(),
		[]*knowledgev1.CollectChunkRequest{positionChunk("T-1"), positionChunk("T-2")}, 1, 2, 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "carries an undeclared type",
		"the server's own refusal is still in the message")
	assert.Contains(t, err.Error(), "This chunk was not written",
		"including its chunk-scoped sentence, which the collect-scoped one sits beside rather than replacing")
}

// TestUploadChunks_ASUCCESSFULUploadCarriesNoPositionSentence is the negative
// control: the sentence is a failure report, not something every collect prints.
func TestUploadChunks_ASUCCESSFULUploadCarriesNoPositionSentence(t *testing.T) {
	client, rec := startRecordingIngest(t)
	sink := NewUploadSink(client)

	err := sink.uploadChunks(context.Background(), "acme", collectResultForPosition(),
		[]*knowledgev1.CollectChunkRequest{positionChunk("T-1"), positionChunk("T-2")}, 1, 2, 0)

	require.NoError(t, err)
	assert.Equal(t, 2, chunkCount(rec), "both chunks landed, which is what makes the nil error meaningful")
}
