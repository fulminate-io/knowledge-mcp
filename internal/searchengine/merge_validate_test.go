// SPDX-License-Identifier: Apache-2.0

package searchengine

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// merge_validate_test.go — a merge whose output does not validate publishes
// nothing, keeps its constituents, and says so.
//
// THE DOUBLE REFUSES ITS OWN OUTPUT, which is what makes this a test of the GATE
// rather than of a format. invalidOutputFormat merges exactly as mockFormat does —
// the merged payload decodes, its rows are correct, an engine handed it would serve
// it — and its validator refuses it while a latch is set. So the only thing standing
// between that payload and the published set is the gate, and turning the latch off
// is the control that proves the same merge publishes when validation passes.

// invalidOutputFormat is mockFormat with a validator that can be told to refuse.
type invalidOutputFormat struct {
	mockFormat
	refuse atomic.Bool
}

// errInjectedInvalidOutput is what the double's validator reports while latched.
//
// IT IS A *CorruptSegmentError BECAUSE THAT IS WHAT A REAL VALIDATOR RETURNS —
// bm25.ValidateSegment and hnsw.ValidateSegment both convert their formats' raised
// invariants into this type — and the type is exactly what makes doMerge's arm order
// load-bearing: the refusal WRAPS it, so a caller testing for a stored corruption
// first would match through Unwrap and hand the owner an id that was never stored.
// A double returning a plain error could not exercise that at all.
var errInjectedInvalidOutput = &CorruptSegmentError{
	Detail: "injected: the merged payload is structurally invalid",
}

func (f *invalidOutputFormat) ValidateSegment(SegmentID, []byte) error {
	if f.refuse.Load() {
		return errInjectedInvalidOutput
	}
	return nil
}

// importTwoMergeable seeds an engine with two single-document segments and returns
// their entries, which is the input every row below merges.
func importTwoMergeable(t *testing.T, e *SegmentedIndex[mockQuery, mockStats]) []*segmentEntry[mockQuery, mockStats] {
	t.Helper()
	require.NoError(t, e.Import([]SegmentBlob{
		{ID: "in-1", Bytes: []byte(`[{"id":"a","content":"alpha"}]`)},
		{ID: "in-2", Bytes: []byte(`[{"id":"b","content":"alpha"}]`)},
	}, nil))
	set := e.set.Load()
	require.Len(t, set.entries, 2, "the fixture must start with two mergeable segments")
	return set.entries
}

// residentIDs lists the published segment ids, sorted, so a row can state exactly
// which segments are being served.
func residentIDs(e *SegmentedIndex[mockQuery, mockStats]) []SegmentID {
	set := e.set.Load()
	out := make([]SegmentID, 0, len(set.entries))
	for _, entry := range set.entries {
		out = append(out, entry.meta.ID)
	}
	return out
}

// TestMergeOutputThatDoesNotValidateIsNeverPublished is the requirement's row: the
// output is discarded, the constituents stay resident AND searchable, and the error
// names both.
func TestMergeOutputThatDoesNotValidateIsNeverPublished(t *testing.T) {
	format := &invalidOutputFormat{}
	e := closeOnCleanup(t, New[mockQuery, mockStats](format, Options{
		MinSegmentDocs: 1,
		// The background merger must not race the two explicit merges below.
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: 1 << 20,
	}))
	chosen := importTwoMergeable(t, e)
	before := residentIDs(e)

	// THE TREATMENT: the validator refuses this merge's output.
	format.refuse.Store(true)
	e.doMerge(chosen)

	require.Equal(t, before, residentIDs(e),
		"a refused merge output must leave the published set exactly as it was — both constituents, same ids")
	hits := e.Search(mockQuery{term: "alpha"}, 10)
	require.Len(t, hits, 2, "both constituents must still answer a search after the merge was refused")

	// AND THE ERROR NAMES BOTH: the segments that stayed, and what was discarded.
	// doMerge logs and returns, so the message is taken from the call it makes.
	entry, err := e.mergeEntry(segmentsOf(chosen), acceptAll(len(chosen)), before)
	require.Nil(t, entry)
	require.Error(t, err)
	var invalid *MergeOutputInvalidError
	require.ErrorAs(t, err, &invalid, "the refusal must be typed, so a caller can route it rather than guess")
	require.Equal(t, before, invalid.Constituents)
	require.ErrorIs(t, err, errInjectedInvalidOutput, "the validator's own reason must survive the wrapping")
	for _, id := range before {
		require.Contains(t, err.Error(), id, "the refusal must name constituent %s", id)
	}
	require.Contains(t, err.Error(), "stay resident and searchable")
	require.NotZero(t, invalid.Bytes, "the refusal states the size of what it discarded")

	// THE CONTROL, without which the row above proves only that something failed:
	// the same merge, the same constituents, validation passing — and it publishes.
	format.refuse.Store(false)
	e.doMerge(chosen)
	require.Len(t, residentIDs(e), 1,
		"with the validator passing, the same merge must consolidate the two constituents into one segment")
	require.Len(t, e.Search(mockQuery{term: "alpha"}, 10), 2,
		"and the consolidated segment still answers for both documents")
}

// segmentsOf projects entries onto their payloads, which is what mergeEntry takes.
func segmentsOf(entries []*segmentEntry[mockQuery, mockStats]) []Segment[mockQuery, mockStats] {
	out := make([]Segment[mockQuery, mockStats], 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.payload)
	}
	return out
}

// acceptAll keeps every member of every input, so the merge under test is the whole
// consolidation rather than a filtered one.
func acceptAll(n int) []func(ExternalID) bool {
	out := make([]func(ExternalID) bool, n)
	for i := range out {
		out[i] = func(ExternalID) bool { return true }
	}
	return out
}

// TestMergeOutputRefusalIsNotAQuarantineSignal pins the disposition split that the
// two error types exist for: a refused merge OUTPUT must not reach the owner as a
// corruption to quarantine, because no such segment was ever stored.
//
// IT IS THE ARM ORDER IN doMerge THAT MAKES THIS TRUE. The refusal wraps the
// validator's error, so a caller testing for the stored-corruption type first would
// match through Unwrap and hand the owner an unattributed id — and the owner's
// quarantine would then refuse it loudly, or worse, withdraw a healthy segment.
func TestMergeOutputRefusalIsNotAQuarantineSignal(t *testing.T) {
	format := &invalidOutputFormat{}
	var reported []*CorruptSegmentError
	e := closeOnCleanup(t, New[mockQuery, mockStats](format, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: 1 << 20,
		OnCorruptSegment:   func(err *CorruptSegmentError) { reported = append(reported, err) },
	}))
	chosen := importTwoMergeable(t, e)

	format.refuse.Store(true)
	e.doMerge(chosen)

	require.Empty(t, reported,
		"a merge output that failed validation is not a stored corruption: there is no file to quarantine and no id to withdraw")
	require.Len(t, residentIDs(e), 2, "and both constituents are still published")
}

// TestBucketSwapWithARefusedMergeOutputReportsNoCorruption is the COMPOSITION row
// with the resident-bound work landed beside this one: every merging operation now
// routes its error through reportCorruptFrom so a corrupt CONSTITUENT found by a
// bucket swap is withdrawn instead of failing that partition forever.
//
// A REFUSED MERGE OUTPUT MUST NOT TAKE THAT ROUTE. It wraps the validator's
// *CorruptSegmentError, so the type test inside that seam matches it through Unwrap
// — and the segment it would name was never published, has no id, and does not exist
// on disk. The discrimination lives in reportCorruptFrom itself rather than in each
// caller's arm order, which is what makes THIS path correct without ReplaceBucket
// knowing anything about merge-output validation.
func TestBucketSwapWithARefusedMergeOutputReportsNoCorruption(t *testing.T) {
	format := &invalidOutputFormat{}
	var reported []*CorruptSegmentError
	e := closeOnCleanup(t, New[mockQuery, mockStats](format, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: 1 << 20,
		OnCorruptSegment:   func(err *CorruptSegmentError) { reported = append(reported, err) },
	}))
	importTwoMergeable(t, e)
	before := residentIDs(e)

	format.refuse.Store(true)
	id, err := e.ReplaceBucket(0, 1, before, nil, nil)

	require.Error(t, err, "the swap must fail rather than publish an unreadable segment")
	var invalid *MergeOutputInvalidError
	require.ErrorAs(t, err, &invalid,
		"and it must reach the caller as the refusal, which is the error the bound's skip path acts on")
	require.Empty(t, id, "nothing was published")
	require.Empty(t, reported,
		"a refused merge OUTPUT is not a stored corruption: reporting it here would hand the owner an id that was never stored")
	require.Equal(t, before, residentIDs(e), "and both constituents are still published and searchable")
	require.Len(t, e.Search(mockQuery{term: "alpha"}, 10), 2)

	// THE CONTROL IN THE SAME INSTRUMENT: with the validator passing, the same swap
	// publishes — so the assertions above are about the refusal and not about a swap
	// that could never have worked.
	format.refuse.Store(false)
	id, err = e.ReplaceBucket(0, 1, before, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.Empty(t, reported)
}

// mergeLogHandler collects the records the merge path emits, with attributes
// flattened, so a row can assert on the LEVEL and the wording an operator sees
// rather than on a render.
type mergeLogHandler struct {
	mu      sync.Mutex
	records []mergeLogRecord
}

type mergeLogRecord struct {
	Level slog.Level
	Msg   string
	Attrs map[string]string
}

func (h *mergeLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *mergeLogHandler) Handle(_ context.Context, r slog.Record) error {
	rec := mergeLogRecord{Level: r.Level, Msg: r.Message, Attrs: make(map[string]string, r.NumAttrs())}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

func (h *mergeLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *mergeLogHandler) WithGroup(string) slog.Handler      { return h }

// errorsMatching returns the captured ERROR records whose message contains want.
//
// THE LEVEL IS FIXED RATHER THAN A PARAMETER because every row here is about a
// disposition line an operator must see: a merge refusal and a corrupt-constituent
// report are both ERROR by design, and a caller that could lower the threshold could
// assert on a line that shipped at INFO. A row needing another level adds the case
// with the parameter.
func (h *mergeLogHandler) errorsMatching(want string) []mergeLogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []mergeLogRecord
	for _, r := range h.records {
		if r.Level >= slog.LevelError && strings.Contains(r.Msg, want) {
			out = append(out, r)
		}
	}
	return out
}

// captureMergeLogs installs a record-collecting default logger for the test.
func captureMergeLogs(t *testing.T) *mergeLogHandler {
	t.Helper()
	h := &mergeLogHandler{}
	prior := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return h
}

// TestRefusedMergeOutputIsLoggedAsAnErrorThatPublishedNothing observes the merger's
// own disposition line, which is the only thing an operator sees when a background
// merge is refused: doMerge returns without a caller.
//
// THE LEVEL AND THE WORDING ARE THE ASSERTION, not the fact that something was
// logged. A refused merge output is a producer defect on a data path, so it is an
// ERROR; and the line has to say that nothing was published and nothing needs
// quarantining, because the generic "segment merge failed" warning this arm replaces
// says neither and leaves an operator to guess whether a segment is now missing.
func TestRefusedMergeOutputIsLoggedAsAnErrorThatPublishedNothing(t *testing.T) {
	logs := captureMergeLogs(t)
	format := &invalidOutputFormat{}
	e := closeOnCleanup(t, New[mockQuery, mockStats](format, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: 1 << 20,
	}))
	chosen := importTwoMergeable(t, e)

	format.refuse.Store(true)
	e.doMerge(chosen)

	got := logs.errorsMatching("produced an unreadable segment and published nothing")
	require.Len(t, got, 1,
		"a refused merge must leave exactly one ERROR line saying it published nothing; a generic merge-failed warning is not that line")
	require.Contains(t, got[0].Attrs["note"], "nothing is quarantined")
	require.Equal(t, "2", got[0].Attrs["constituents"])

	// THE CONTROL: a merge that succeeds leaves no such line, so the assertion above
	// is about the refusal rather than about any merge at all.
	format.refuse.Store(false)
	e.doMerge(chosen)
	require.Len(t, logs.errorsMatching("produced an unreadable segment and published nothing"), 1,
		"a successful merge adds no refusal line")
}

// raisingValidatorFormat is mockFormat whose validator RAISES rather than returns,
// which is what a format's read path does when it meets a violated invariant.
//
// BOTH SHIPPED VALIDATORS CATCH INTERNALLY, so this shape is reachable only by a
// future format — and that is exactly why it is worth pinning: the classification
// must be a property of the gate, not of the two implementations that happen to
// return today.
type raisingValidatorFormat struct {
	mockFormat
	raise atomic.Bool
}

func (f *raisingValidatorFormat) ValidateSegment(SegmentID, []byte) error {
	if f.raise.Load() {
		RaiseCorrupt("injected: the merged payload violates an invariant the validator raises on")
	}
	return nil
}

// TestAValidatorThatRaisesIsStillARefusedOutputNotACorruptConstituent closes the hole
// a containment boundary would leave: a raise converted inside the boundary comes
// back as a bare *CorruptSegmentError, which every caller's corruption arm matches —
// and the segment it would blame is a healthy CONSTITUENT while the defect is in the
// OUTPUT this process just wrote.
func TestAValidatorThatRaisesIsStillARefusedOutputNotACorruptConstituent(t *testing.T) {
	logs := captureMergeLogs(t)
	format := &raisingValidatorFormat{}
	var reported []*CorruptSegmentError
	e := closeOnCleanup(t, New[mockQuery, mockStats](format, Options{
		MinSegmentDocs:     1,
		DeletesPctAllowed:  MergeDisabledDeadRatio,
		SegmentCountTarget: 1 << 20,
		OnCorruptSegment:   func(err *CorruptSegmentError) { reported = append(reported, err) },
	}))
	chosen := importTwoMergeable(t, e)
	before := residentIDs(e)

	format.raise.Store(true)
	entry, err := e.mergeEntry(segmentsOf(chosen), acceptAll(len(chosen)), before)
	require.Nil(t, entry)
	var invalid *MergeOutputInvalidError
	require.ErrorAs(t, err, &invalid,
		"a raised validation failure must carry the same type a returned one does, or callers dispatch on the shape of the validator")
	require.Equal(t, before, invalid.Constituents)

	// AND IT MUST NOT HAVE BEEN REPORTED AS A STORED CORRUPTION on the way out: the
	// boundary that converts the raise must not also route it to the owner.
	require.Empty(t, reported,
		"a refused merge OUTPUT names no stored segment, so nothing may be handed to the owner to quarantine")

	e.doMerge(chosen)
	require.Empty(t, logs.errorsMatching("aborted by a corrupt constituent"),
		"the constituents are healthy: blaming them for a defect in the output is the misclassification this row exists for")
	require.Len(t, logs.errorsMatching("produced an unreadable segment and published nothing"), 1)
	require.Equal(t, before, residentIDs(e), "and both constituents are still published")
}
