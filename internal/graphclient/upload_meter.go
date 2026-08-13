// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"context"
	"net"
	"sync/atomic"
	"time"
)

// Process-wide socket-write counters. Package-level rather than per-connection
// deliberately: the consumer takes a DELTA across one RPC and never needs to
// know WHICH pooled connection served it, which removes any need to correlate a
// conn back to a request — machinery h2 multiplexing would make unreliable
// anyway.
var (
	socketWrites   atomic.Int64
	socketBytes    atomic.Int64
	socketInWriteN atomic.Int64 // nanoseconds spent inside Write
)

// timedConn wraps a raw network connection and accumulates, for every Write:
// the call count, the bytes accepted, and the wall time spent inside the
// underlying Write. It exists to answer one question a duration alone cannot —
// when a chunk upload is slow, were the bytes held on OUR side of the socket or
// upstream of it?
//
// HOW TO READ THESE NUMBERS, in both directions, because h2 multiplexes and
// these counters aggregate every stream on every pooled connection. A delta
// taken across one chunk therefore includes any concurrent freshness or
// pipeline traffic sharing the connection.
//
//   - FALSE POSITIVES are structurally impossible in the direction that
//     matters: foreign traffic can only ADD time-inside-Write, so a NEAR-ZERO
//     reading during a slow chunk stays trustworthy. That is the reading the
//     loud client-side-stall line is built on.
//   - FALSE NEGATIVES are possible, and this is the caveat to carry: heavy
//     concurrent traffic — a pipeline drain writing on the same connection —
//     can push the measured in-Write time past the flagging fraction during a
//     genuine client-side stall and SUPPRESS the loud line. The mitigation is
//     already in place rather than needing new machinery: the raw delta rides
//     the per-chunk debug record unconditionally, so a suppressed loud line is
//     still reconstructable from the debug log after the fact. Per-stream
//     attribution would need h2 internals neither net/http's bundled h2 nor
//     golang.org/x/net/http2 exposes.
//
// Overhead is two atomic adds and one time.Since per socket Write — a few
// hundred atomic operations per multi-megabyte chunk. That is the whole reason
// this can stay permanently armed instead of hiding behind a flag.
type timedConn struct {
	net.Conn
}

// Write times the wrapped write and folds the result into the process-wide
// counters. Bytes are counted as REPORTED WRITTEN, so a short write contributes
// only what actually left.
func (c *timedConn) Write(b []byte) (int, error) {
	start := time.Now()
	n, err := c.Conn.Write(b)
	socketInWriteN.Add(int64(time.Since(start)))
	socketWrites.Add(1)
	socketBytes.Add(int64(n))
	return n, err
}

// dialInstrumented is the DialContext for the shared cloud transport. It wraps
// the RAW TCP connection: the transport layers TLS ABOVE whatever the dialer
// returns, so ALPN and h2 negotiation are untouched and the timed writes are
// TLS records leaving for the kernel.
//
// A custom DialContext would conservatively disable HTTP/2 on its own — the
// transport that installs this keeps ForceAttemptHTTP2 true, which is what
// preserves h2 beside it.
func dialInstrumented(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return &timedConn{Conn: conn}, nil
}

// Thresholds for the client-side-stall discriminator. Both are anchored on
// measurement rather than taste:
//
//   - StallElapsedThreshold: healthy 4 MiB chunks measured 188-737ms across 30
//     probe runs and 1.1-3.3s at the load balancer in production, while the
//     failure population sits at 7.80s p90 for a first-position chunk and the
//     observed cut lands at 10.05s plus a rate-dependent term. 5s is above every
//     healthy observation and below every observed cut.
//   - StallWriteFractionDivisor: the measured separation is three orders of
//     magnitude — the FASTEST healthy transfer spent 81.5ms of 500ms inside
//     Write (16%), the reproduced failure 6.3ms of 15.3s (0.04%). Any threshold
//     between roughly 1% and 15% separates them; a divisor of 20 puts the line
//     at 5%, near the geometric middle, so it is not tuned to a single run.
const (
	StallElapsedThreshold     = 5 * time.Second
	StallWriteFractionDivisor = 20
)

// ShouldFlagClientSideStall reports whether one chunk upload carries the
// client-side-stall signature: a long elapsed time of which almost none was
// spent inside the socket Write. That means the bytes were held on OUR side of
// the socket, and the search moves to the Go runtime (GC assist, scheduler,
// memory pressure). Long elapsed WITH substantial time inside Write means the
// path is back-pressuring instead, and the search moves upstream.
//
// True iff elapsed >= StallElapsedThreshold AND inWrite < elapsed divided by
// StallWriteFractionDivisor. The elapsed floor is inclusive and the fraction is
// exclusive.
func ShouldFlagClientSideStall(elapsed, inWrite time.Duration) bool {
	return elapsed >= StallElapsedThreshold && inWrite < elapsed/StallWriteFractionDivisor
}

// SocketWriteStats is one reading of the process-wide socket-write counters.
// Only DIFFERENCES between two readings are meaningful; the absolute values are
// whatever this process has accumulated since start.
type SocketWriteStats struct {
	Writes  int64
	Bytes   int64
	InWrite time.Duration
}

// SocketWriteSnapshot reads the counters. Exported because the consumer — the
// collect upload sink — lives in a different package.
//
// The three counters are read independently, so a snapshot taken while another
// goroutine is mid-Write can straddle that write. It is a measurement, not a
// ledger, and a single write's worth of skew does not move a discriminator
// separating milliseconds from seconds.
func SocketWriteSnapshot() SocketWriteStats {
	return SocketWriteStats{
		Writes:  socketWrites.Load(),
		Bytes:   socketBytes.Load(),
		InWrite: time.Duration(socketInWriteN.Load()),
	}
}
