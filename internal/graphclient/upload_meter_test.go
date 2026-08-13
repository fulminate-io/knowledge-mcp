// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShouldFlagClientSideStall_Discriminator pins the signature the whole
// instrument exists to detect: a chunk that took a long time while almost none
// of that time was spent inside the socket Write.
//
// PROVENANCE OF EACH ROW IS LABELED, because two of these pairs are real
// measurements and two are constructed, and a reader must not mistake one for
// the other.
func TestShouldFlagClientSideStall_Discriminator(t *testing.T) {
	tests := []struct {
		name     string
		elapsed  time.Duration
		inWrite  time.Duration
		want     bool
		provenue string
	}{
		{
			name:     "fast healthy transfer",
			elapsed:  500 * time.Millisecond,
			inWrite:  81500 * time.Microsecond, // 81.5ms
			want:     false,
			provenue: "MEASURED: the fastest healthy probe transfer, the only one showing real socket back-pressure (16% of elapsed inside Write). Below the elapsed floor anyway.",
		},
		{
			name:     "reproduced client-side stall",
			elapsed:  15300 * time.Millisecond, // 15.3s
			inWrite:  6300 * time.Microsecond,  // 6.3ms
			want:     true,
			provenue: "MEASURED: the reproduced failure — 0.04% of elapsed inside Write. This is the signature the loud line is built on.",
		},
		{
			name:     "network-bound slow transfer",
			elapsed:  15300 * time.Millisecond,
			inWrite:  4 * time.Second,
			want:     false,
			provenue: "CONSTRUCTED, not observed: a hypothetical genuinely path-bound slow transfer. Included because misattributing THIS case is the failure mode that matters.",
		},
		{
			name:     "slowest healthy probe duration",
			elapsed:  737 * time.Millisecond,
			inWrite:  1 * time.Millisecond,
			want:     false,
			provenue: "CONSTRUCTED: the slowest healthy probe duration (measured) paired with an INVENTED Write time. Still under the elapsed floor, so a healthy-but-slow chunk stays quiet.",
		},
		{
			name:     "exactly at the elapsed threshold, just under the fraction",
			elapsed:  StallElapsedThreshold,
			inWrite:  StallElapsedThreshold/StallWriteFractionDivisor - time.Nanosecond,
			want:     true,
			provenue: "CONSTRUCTED boundary: the threshold is inclusive on elapsed and exclusive on the fraction.",
		},
		{
			name:     "exactly at the elapsed threshold, exactly at the fraction",
			elapsed:  StallElapsedThreshold,
			inWrite:  StallElapsedThreshold / StallWriteFractionDivisor,
			want:     false,
			provenue: "CONSTRUCTED boundary: at the fraction is NOT under it.",
		},
		{
			name:     "just under the elapsed threshold with near-zero write time",
			elapsed:  StallElapsedThreshold - time.Nanosecond,
			inWrite:  0,
			want:     false,
			provenue: "CONSTRUCTED boundary: the elapsed floor is what keeps fast chunks quiet no matter how little time they spend inside Write.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ShouldFlagClientSideStall(tc.elapsed, tc.inWrite), tc.provenue)
		})
	}
}

// TestSocketWriteMeter_CountsWriteTimeAndBytes drives a timedConn over a local
// pipe and asserts the snapshot advanced by exactly what this test wrote. Every
// expected number is derived from the payload the test itself sends — never a
// remembered constant — because the counters are process-wide and any absolute
// assertion would be a hostage to whatever else touched them.
func TestSocketWriteMeter_CountsWriteTimeAndBytes(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	// Drain the far end so the pipe writes complete.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1024)
		for {
			if _, err := server.Read(buf); err != nil {
				return
			}
		}
	}()

	tc := &timedConn{Conn: client}

	payloads := [][]byte{
		[]byte("first"),
		[]byte("second payload"),
		make([]byte, 512),
	}
	var wantBytes int64
	for _, p := range payloads {
		wantBytes += int64(len(p))
	}

	before := SocketWriteSnapshot()
	for _, p := range payloads {
		n, err := tc.Write(p)
		require.NoError(t, err)
		require.Equal(t, len(p), n, "timedConn must not swallow or short-count a write")
	}
	after := SocketWriteSnapshot()

	assert.Equal(t, int64(len(payloads)), after.Writes-before.Writes,
		"one counted Write per Write call")
	assert.Equal(t, wantBytes, after.Bytes-before.Bytes,
		"bytes advance by exactly what this test wrote")
	assert.GreaterOrEqual(t, after.InWrite-before.InWrite, time.Duration(0),
		"time spent inside Write is never negative")

	_ = client.Close()
	<-done
}
