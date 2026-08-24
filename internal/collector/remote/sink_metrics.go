// SPDX-License-Identifier: Apache-2.0

package remote

import (
	"log/slog"
	"time"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// sink_metrics.go holds the upload path's MEASUREMENT and conversion helpers —
// socket-write deltas, duration rendering, the client-side stall log and the
// proto→value edge conversion — split out of sink.go for the 500-line file cap.
//
// None of them touch the wire or the diff decision; they are what WriteResult
// reports WITH, not what it decides ON.

// meterDelta returns how far the process-wide socket-write counters advanced
// since before. Only the difference is meaningful — the absolute counters carry
// whatever this process accumulated since start.
func meterDelta(before graphclient.SocketWriteStats) graphclient.SocketWriteStats {
	now := graphclient.SocketWriteSnapshot()
	return graphclient.SocketWriteStats{
		Writes:  now.Writes - before.Writes,
		Bytes:   now.Bytes - before.Bytes,
		InWrite: now.InWrite - before.InWrite,
	}
}

// millis renders a duration as fractional milliseconds, keeping sub-millisecond
// readings legible — the client-side-stall signature is precisely a reading of a
// few milliseconds against an elapsed measured in seconds, and truncating that
// to an integer would print the most interesting value as 0.
func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

// logClientSideStall emits the one loud line for a chunk carrying the
// client-side-stall signature. It is INFO, not WARN: nothing is broken when it
// fires — the collect may well have succeeded — and a WARN would train the
// operator to ignore the one instrument armed for a rare event.
//
// Extracted rather than written inline because the emission IS the deliverable:
// inline, a name-grep for the predicate passes even on a discarded call with no
// logging at all, and an instrument that never fires is indistinguishable from
// one that was never wired. As a helper it is directly drivable from a test.
// Keep it a formatter over the record below; it must not grow logic.
func logClientSideStall(i, of, bytes int, elapsed, inWrite time.Duration, writes int64) {
	slog.Info("remote sink: chunk was slow while almost no time was spent writing to the socket — "+
		"the bytes were held CLIENT-SIDE, not by the network path",
		"i", i, "of", of, "bytes", bytes,
		"dur", elapsed.Round(time.Millisecond),
		"in_write_ms", millis(inWrite),
		"socket_writes", writes,
		"next", "re-run the collect with GODEBUG=http2debug=2 to capture h2 frame detail on the next occurrence")
}

// edgesFromProto converts the typed proto Edge carrier into []knowledgev1.Edge —
// the remote-package decode for the FetchCloudSubgraph slice edges (the value
// shape cloudresolver.GraphSlice.Edges expects). Mirrors the engine package's
// EdgesFromProto (kept local so the collector/remote package does not depend on
// the engine decode package). Empty carrier → nil.
func edgesFromProto(in []*knowledgev1.Edge) []knowledgev1.Edge {
	if len(in) == 0 {
		return nil
	}
	out := make([]knowledgev1.Edge, len(in))
	for i, e := range in {
		out[i] = knowledgev1.Edge{
			FromId:        e.GetFromId(),
			ToId:          e.GetToId(),
			Type:          e.GetType(),
			Weight:        e.GetWeight(),
			Confidence:    e.GetConfidence(),
			Method:        e.GetMethod(),
			Evidence:      e.GetEvidence(),
			LastValidated: e.GetLastValidated(),
		}
	}
	return out
}
