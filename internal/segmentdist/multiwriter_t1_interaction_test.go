// SPDX-License-Identifier: Apache-2.0

package segmentdist

import (
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

// multiwriter_t1_interaction_test.go proves the T1 bounded-streaming fix (the Fetch
// byte-ceiling + client adaptive halve-and-retry) holds under MULTI-WRITER
// concurrent cold-loads. K writers concurrently cold-fetch the same large id-set,
// each through the existing recordingFetchSource byte-ceiling instrumentation
// (manager_fetchmisses_test.go), and the suite asserts under contention: no single
// Fetch ever carries more than maxFetchSegmentIDs ids, the client halves+retries on
// CodeResourceExhausted until EVERY blob imports (no loss) for EVERY writer, and the
// adaptive split was genuinely exercised (an over-threshold chunk was rejected and
// halved). A retry-under-contention regression dropping a blob fails the no-loss arm.

// TestMultiWriterT1ConcurrentColdLoads runs K concurrent cold-loads, each driving
// fetchMisses through a byte-ceiling caller that rejects over-threshold chunks.
func TestMultiWriterT1ConcurrentColdLoads(t *testing.T) {
	t.Parallel()

	const (
		k         = 4
		threshold = 64                        // byte-ceiling stand-in: > threshold ids ⇒ ResourceExhausted
		nIDs      = 3*maxFetchSegmentIDs + 11 // several count-capped chunks, not a clean multiple
	)
	ids := segIDs(nIDs)

	// Each writer gets its OWN byte-ceiling caller + manager so the per-writer
	// no-loss + call-count instrumentation is isolated, while all K run concurrently.
	callers := make([]*recordingFetchSource, k)
	mgrs := make([]*distManager[mockQuery, mockStats], k)
	for w := range k {
		callers[w] = &recordingFetchSource{reject: func(reqIDs []string) error {
			if len(reqIDs) > threshold {
				return connect.NewError(connect.CodeResourceExhausted,
					fmt.Errorf("byte ceiling: %d ids too large", len(reqIDs)))
			}
			return nil
		}}
		mgrs[w] = newFetchMissesManager(t, callers[w])
	}

	// All K writers cold-fetch the SAME large id-set concurrently.
	type result struct {
		blobs []blobImport
		err   error
	}
	results := make([]result, k)
	var wg sync.WaitGroup
	for w := range k {
		wg.Go(func() {
			blobs, err := mgrs[w].fetchMisses(t.Context(), ids)
			imported := make([]blobImport, len(blobs))
			for i, b := range blobs {
				imported[i] = blobImport{id: b.ID, bytes: string(b.Bytes)}
			}
			results[w] = result{blobs: imported, err: err}
		})
	}
	wg.Wait()

	for w := range k {
		require.NoError(t, results[w].err, "writer %d: halving must fit every sub-chunk under the ceiling", w)

		// NO LOSS: every id imported, in input order, with its bytes intact.
		require.Len(t, results[w].blobs, nIDs, "writer %d: every blob must import (no loss under contention)", w)
		for i, b := range results[w].blobs {
			require.Equalf(t, ids[i], b.id, "writer %d: blob %d out of order or missing", w, i)
			require.Equalf(t, ids[i], b.bytes, "writer %d: blob %d bytes corrupted", w, i)
		}

		// CAP: every SUCCEEDING Fetch carried <= maxFetchSegmentIDs ids; the adaptive
		// split was exercised (an over-threshold chunk was rejected + halved).
		counts := callers[w].callCounts()
		require.NotEmpty(t, counts, "writer %d issued Fetches", w)
		sawHalved := false
		for _, c := range counts {
			require.LessOrEqualf(t, c, maxFetchSegmentIDs, "writer %d: a Fetch exceeded maxFetchSegmentIDs", w)
			if c > threshold {
				sawHalved = true // a rejected (then halved) attempt
			}
		}
		require.Truef(t, sawHalved, "writer %d: the adaptive split must have been exercised (an over-threshold chunk rejected)", w)
	}
}

// blobImport is a value snapshot of an imported blob so the concurrent fetch results
// can be asserted after the goroutines join without aliasing the engine carriers.
type blobImport struct {
	id    string
	bytes string
}
