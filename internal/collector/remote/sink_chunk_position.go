// SPDX-License-Identifier: Apache-2.0

// sink_chunk_position.go — WHAT A FAILED CHUNK SAYS ABOUT THE CHUNKS BEFORE IT.
//
// A collect uploads its rows in several CollectChunk calls, and each one lands in
// its own server-side batch. So a failure part-way through leaves every earlier
// chunk IN THE GRAPH, and the caller's only signal is the error the upload loop
// returns.
//
// THE LIVE DEFECT THIS FIXES. The server's undeclared-type refusal used to end
// with "Nothing was written". A probe emitting two declared nodes and one
// undeclared edge was refused on chunk 2 of 2 with that sentence while chunk 1's
// two nodes were already resident — so the operator was told a half-written graph
// did not exist, and had no reason to go looking for it. The server has since been
// narrowed to the only claim it can prove, "This chunk was not written", because
// CollectChunkRequest carries no chunk index and no chunk total: the position
// within a collect is knowledge THIS end has and the server does not.
//
// IT APPLIES TO EVERY CHUNK FAILURE, not only to the refusal that exposed it. The
// arithmetic is a property of the chunked upload rather than of any one error: a
// failure on chunk N means chunks 1..N-1 landed, whatever failed the Nth. A
// permission denial, a transport give-up and a refusal all leave the same partial
// graph behind.
//
// IT IS NOT A CLAIM THAT THE COLLECT IS FINISHED WITH THOSE ROWS. They stand until
// the NEXT collect of the same graph, whose epoch sweep is what reconciles them —
// which is why the sentence says so rather than implying the operator must clean
// up by hand.

package remote

import "fmt"

// chunkPositionStatement renders the collect-scoped truth about a failure at
// ZERO-BASED chunk index i: what, if anything, of this collect is in the graph.
//
// THE FIRST CHUNK IS ITS OWN SENTENCE rather than "0 earlier chunks", because
// nothing written at all is the one case where the operator has nothing to look
// for, and a zero embedded in the plural sentence reads as a partial write.
func chunkPositionStatement(i int) string {
	if i == 0 {
		return "no chunk of this collect was written"
	}
	noun := "chunks"
	verb := "stand"
	if i == 1 {
		noun, verb = "chunk", "stands"
	}
	return fmt.Sprintf("%d earlier %s of this collect already landed and %s until the next collect",
		i, noun, verb)
}
