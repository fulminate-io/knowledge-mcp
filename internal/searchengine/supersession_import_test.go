package searchengine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// supersession_import_test.go is the behavioral half of the durable supersession
// record: a merge's consolidated blob CARRIES the ids it replaced, and an import that
// sees that blob declines its constituents.
//
// THE COLD IMPORT IS THE WHOLE POINT, and it is why these tests build a SECOND engine
// rather than asserting against the one that merged. The engine that ran the merge
// already dropped the constituents through its own CAS; only a fresh engine handed the
// STORED corpus — merged blob and constituents together, exactly as an L2 index holds
// them across an un-reclaimed merge window — proves the record survived the round trip
// and is honored with no external state at all.

// mergedWithConstituents drives one real merge and returns the constituent blobs and
// the consolidated blob that superseded them.
func mergedWithConstituents(t *testing.T) (constituents []SegmentBlob, merged SegmentBlob) {
	t.Helper()

	recorder := &captureOnMerge{}
	// One doc per segment, and a count target of 1 so two segments merge immediately.
	e := callbackMergeEngine(t, recorder.fn(), 2.0, 1)
	require.NoError(t, e.Add([]Document{doc("keep-a", "x")}))
	pre := e.Export()
	require.Len(t, pre, 1, "FIXTURE: the first Add must seal a segment of its own")
	constituents = append(constituents, pre[0])

	require.NoError(t, e.Add([]Document{doc("keep-b", "x")}))
	for _, b := range e.Export() {
		if b.ID != constituents[0].ID {
			constituents = append(constituents, b)
		}
	}
	require.Len(t, constituents, 2, "FIXTURE: two single-doc constituents before the merge")

	waitForMerge(t, e) // FIXTURE: the background merge must actually fire
	results := pollResults(recorder, 1)
	require.Len(t, results, 1, "FIXTURE: exactly one merge")
	require.Len(t, results[0].Removed, 2, "FIXTURE: it must supersede both constituents")
	return constituents, results[0].Merged
}

func TestAMergedBlobRecordsWhatItSuperseded(t *testing.T) {
	t.Parallel()

	constituents, merged := mergedWithConstituents(t)

	// THE RECORD IS IN Envelope, not in Bytes. Bytes is the format payload by the
	// invariant on SegmentBlob, so reading it here would find no envelope and report
	// an empty record for a blob that carries one.
	recorded, cohort, err := SupersededBy(merged.Envelope)
	require.NoError(t, err)
	require.Equal(t, []SegmentID{merged.ID}, cohort,
		"a background merge publishes ONE output, so its cohort is itself — the proof a reader needs "+
			"that the replacement landed whole")
	require.Len(t, recorded, 2,
		"the consolidated blob must record the constituents it replaced — without it the stored corpus "+
			"says nothing about supersession and a cold load cannot tell a live segment from a replaced one")
	for _, c := range constituents {
		require.Contains(t, recorded, c.ID)
	}

	// CONTROL: a segment that superseded nothing carries NO record, so the ordinary
	// blob stays byte-identical to what this engine wrote before the record existed.
	plain, _, err := SupersededBy(constituents[0].Envelope)
	require.NoError(t, err)
	require.Empty(t, plain, "a constituent supersedes nothing and must carry no envelope")
}

func TestAColdImportDeclinesTheConstituentsTheMergedBlobRecords(t *testing.T) {
	t.Parallel()

	constituents, merged := mergedWithConstituents(t)

	// THE STORED SET, exactly as an L2 index holds it across an un-reclaimed merge
	// window: both constituents AND the blob that superseded them.
	fresh := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs: 1, DeletesPctAllowed: 2.0, SegmentCountTarget: 1 << 30,
	}))
	stored := append(append([]SegmentBlob(nil), constituents...), merged)
	require.NoError(t, fresh.Import(stored, nil))

	resident := fresh.ResidentSegmentIDs()
	require.Equal(t, []SegmentID{merged.ID}, resident,
		"a cold import must publish the consolidated blob ALONE — its record names the other two, and "+
			"publishing them beside it duplicates every document across two segments")

	// KNOWN POSITIVE, same run, same instrument: the documents themselves survive.
	// Without it the assertion above is equally satisfied by an import that dropped
	// everything.
	hits := fresh.Search(mockQuery{term: "x"}, 10)
	require.Len(t, hits, 2, "CONTROL: both documents are still searchable, from the consolidated blob")
}

// TestARecordIsHonouredOnlyWhenItsWholeCohortIsPresent is the gate on the half that
// keeps this record from being a data-loss instrument.
//
// THE HAZARD IT PINS. A group swap publishes several partitions against ONE union
// removal set, and it is the SET that carries the superseded constituents' members
// forward — no single output holds them all. If only part of that set reached disk (a
// crash between two writes, an L2 write that failed) then honoring one output's record
// would retire segments whose only other copy is a sibling that is not there.
func TestARecordIsHonouredOnlyWhenItsWholeCohortIsPresent(t *testing.T) {
	t.Parallel()

	// THREE REAL SEGMENTS, so the decline below is about the record rather than about
	// bytes no format could decode.
	src := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs: 1, DeletesPctAllowed: 2.0, SegmentCountTarget: 1 << 30,
	}))
	for _, id := range []ExternalID{"constituent", "output-x", "output-y"} {
		require.NoError(t, src.Add([]Document{doc(id, "x")}))
	}
	blobs := src.Export()
	require.Len(t, blobs, 3, "FIXTURE: one segment per document")
	constituent, outX, outY := blobs[0], blobs[1], blobs[2]

	// outX claims to supersede the constituent, as one output of a two-output swap.
	// THE ENVELOPE IS SET, NOT PREPENDED TO Bytes. Bytes is the format payload by
	// the invariant on SegmentBlob, so a test that concatenated here would be
	// building a blob no producer produces.
	outX.Envelope = encodeSupersessionPrefix(supersessionRecord{
		Superseded: []SegmentID{constituent.ID},
		Cohort:     []SegmentID{outX.ID, outY.ID},
	})

	t.Run("a partial cohort declines nothing", func(t *testing.T) {
		t.Parallel()
		partial := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
			MinSegmentDocs: 1, DeletesPctAllowed: 2.0, SegmentCountTarget: 1 << 30,
		}))
		require.NoError(t, partial.Import([]SegmentBlob{constituent, outX}, nil))
		require.Contains(t, partial.ResidentSegmentIDs(), constituent.ID,
			"the swap that superseded this constituent did not land whole — its sibling output is "+
				"absent — so retiring the constituent would drop documents nothing else on disk holds")
	})

	t.Run("CONTROL: the whole cohort declines it", func(t *testing.T) {
		t.Parallel()
		whole := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
			MinSegmentDocs: 1, DeletesPctAllowed: 2.0, SegmentCountTarget: 1 << 30,
		}))
		require.NoError(t, whole.Import([]SegmentBlob{constituent, outX, outY}, nil))
		require.NotContains(t, whole.ResidentSegmentIDs(), constituent.ID,
			"CONTROL: with every cohort member present the record IS honored — so the leg above is "+
				"the cohort gate holding rather than a record that never applies")
	})
}

func TestAnImportWithNoRecordPublishesEverything(t *testing.T) {
	t.Parallel()

	// THE ONE-VERSION-BACK CONTROL AT THE IMPORT LEVEL. Blobs written before the
	// record existed carry no envelope, and an import of them must behave exactly as
	// it did then — every blob published, nothing declined.
	constituents, _ := mergedWithConstituents(t)

	fresh := closeOnCleanup(t, New[mockQuery, mockStats](mockFormat{}, Options{
		MinSegmentDocs: 1, DeletesPctAllowed: 2.0, SegmentCountTarget: 1 << 30,
	}))
	require.NoError(t, fresh.Import(constituents, nil))
	require.Len(t, fresh.ResidentSegmentIDs(), 2,
		"record-less blobs are all live by definition — declining one would retire a segment nothing "+
			"superseded")
}
