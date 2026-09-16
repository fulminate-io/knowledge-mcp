// SPDX-License-Identifier: Apache-2.0

// append_stats_race_test.go — a RETAINED OLDER GENERATION's statistics stay readable
// while the append path derives newer ones from them.
//
// WHAT IT PROTECTS, stated as the contract rather than as a hazard nobody has
// reproduced. The incremental shape is only cheap because a generation SHARES its
// predecessor's probe list by reference instead of copying it, and AppendStats' own
// contract is that it MUST NOT MUTATE prev: prev belongs to a published snapshot
// that readers are serving from, and the engine calls this inside a CAS retry loop
// where a losing publisher's work is discarded. A future shape that wrote through to
// the shared structure — accumulating into prev and returning it, reusing prev's
// maps, appending into a shared backing array a reader walks — would break that, and
// the symptom is not a wrong answer on the next call. It is a data race, or a torn
// read on a loaded machine, landing long after the change that introduced it.
//
// AN HONEST LIMIT ON THE INSTRUMENT, recorded because a reader deserves it: the
// backing-array variant of that hazard was NOT reproducible here. Reverting the
// probe list to a slice appended into spare capacity leaves this row green under
// -race, because a generation walks only its OWN prefix of the array and the writer
// writes past it. The chain shape is kept for the cost it removes — a slice cannot
// be extended for the next generation without an O(resident) copy — rather than for
// a race this row demonstrates. What this row DOES catch is proved by its own kill:
// an AppendStats that accumulates into prev turns it red.
//
// It is here rather than in the engine because the shared structure is this
// package's: the engine's entries slice is read prefix-only by every snapshot.
//
// THE READER HOLDS AN OLD GENERATION ON PURPOSE. A reader that re-reads the current
// stats each time could never observe the hazard, because the writer would only be
// racing an object nobody is looking at. These goroutines capture ONE generation
// before the appends begin and resolve document frequencies out of it throughout —
// which is exactly the lifetime a search fanning out over a published snapshot has.
//
// IT IS RUN UNDER -race BY NAME, one specific test per invocation: whole-package
// race belongs to CI.

package bm25

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/searchengine"
)

const (
	raceReaders    = 8
	raceAppends    = 56
	raceWitnessed  = 4
	raceProbeTerms = 6
)

// TestAppendedStatsDoNotWriteAWitnessedGeneration is the race row.
func TestAppendedStatsDoNotWriteAWitnessedGeneration(t *testing.T) {
	f := Format{}

	// The witnessed generation, built BEFORE the writer starts. The readers hold this
	// pointer for the whole run; every generation the writer derives below shares its
	// probe list by reference.
	witnessed := f.AggregateStats(nil)
	for i := range raceWitnessed {
		witnessed = f.AppendStats(witnessed, appendStatsSegment(t,
			[]searchengine.Document{appendStatsDoc(i, fmt.Sprintf("alpha term%d", i))}))
	}
	require.Equal(t, int64(raceWitnessed), witnessed.TotalDocs,
		"PRECONDITION: the witnessed generation must hold documents, or every reader below resolves an empty corpus")
	wantAlpha := witnessed.docFreqOf("alpha")
	require.Positive(t, wantAlpha,
		"PRECONDITION: a term the witnessed generation really answers for, so the readers walk its whole probe list")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for r := range raceReaders {
		wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				// A COLD term every pass, so the walk over the shared probe list is
				// taken rather than answered from the memo — the memo would hide the
				// very read this row exists to expose after the first hit.
				if df := witnessed.docFreqOf(fmt.Sprintf("cold-%d-%d", r, i%raceProbeTerms)); df != 0 {
					panic("the witnessed generation answered a term no document carries")
				}
				_ = witnessed.FieldAvgLen[searchengine.FieldContent]
			}
		})
	}

	// The writer: every append derives a NEW generation from the witnessed chain.
	newest := witnessed
	for i := range raceAppends {
		newest = f.AppendStats(newest, appendStatsSegment(t,
			[]searchengine.Document{appendStatsDoc(raceWitnessed+i, "beta gamma")}))
	}
	close(stop)
	wg.Wait()

	require.Equal(t, int64(raceWitnessed), witnessed.TotalDocs,
		"the witnessed generation is IMMUTABLE: %d appends derived newer ones and must not have grown this one", raceAppends)
	require.Equal(t, wantAlpha, witnessed.docFreqOf("alpha"),
		"and it still answers the frequency it was constructed to answer")
	require.Equal(t, int64(raceWitnessed+raceAppends), newest.TotalDocs,
		"KNOWN POSITIVE: the appends really happened, so the immutability above is not a writer that never ran")
}
