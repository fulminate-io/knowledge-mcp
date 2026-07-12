// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestUploadHealthTracker_Record exercises the two-axis classification: the systemic
// streak (err && nothing shipped) that drives escalation, and the per-file degraded
// signal that is never masked; plus the T4 transport-vs-ship timestamp split and the
// independent-snapshot guarantee.
func TestUploadHealthTracker_Record(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	boom := errors.New("boom")

	t.Run("consent-off advances transport-ok but not ship, resets the streak", func(t *testing.T) {
		tr := NewUploadHealthTracker()
		// Prime a failure streak so the reset is observable.
		tr.Record(Summary{FilesUploaded: 0, Files: []FileSummary{{Err: "x"}}}, boom, now)
		snap := tr.Record(Summary{Skipped: "transcript collection disabled (consent off)"}, nil, now.Add(time.Hour))

		assert.Equal(t, now.Add(time.Hour), snap.LastTransportOK, "consent-off advances the transport clock")
		assert.True(t, snap.LastShip.IsZero(), "consent-off never advances LastShip")
		assert.Equal(t, 0, snap.ConsecutiveFailures, "consent-off resets the systemic streak")
	})

	t.Run("errored tick with zero uploaded increments the systemic streak", func(t *testing.T) {
		tr := NewUploadHealthTracker()
		snap := tr.Record(Summary{FilesUploaded: 0, Files: []FileSummary{{Err: "x"}}}, boom, now)

		assert.Equal(t, 1, snap.ConsecutiveFailures)
		assert.Equal(t, "boom", snap.LastError)
		assert.Equal(t, now, snap.LastFailure)
		assert.True(t, snap.LastShip.IsZero(), "a systemic failure ships nothing")
		assert.True(t, snap.LastTransportOK.IsZero(), "a systemic failure does not advance transport-ok")
		assert.Equal(t, int64(1), snap.TotalFailures)
	})

	t.Run("three consecutive systemic failures reach streak 3; a shipped tick resets", func(t *testing.T) {
		tr := NewUploadHealthTracker()
		for i := 1; i <= 3; i++ {
			snap := tr.Record(
				Summary{FilesUploaded: 0, Files: []FileSummary{{Err: "over cap"}}},
				errors.New("over cap"),
				now.Add(time.Duration(i)*time.Minute),
			)
			assert.Equal(t, i, snap.ConsecutiveFailures, "streak advances one per systemic-failing tick")
			assert.Equal(t, 1, snap.FilesFailedLastTick, "the lone stuck file shows on the degraded axis each tick")
		}
		snap := tr.Record(Summary{FilesUploaded: 2, RowsShipped: 10}, nil, now.Add(time.Hour))
		assert.Equal(t, 0, snap.ConsecutiveFailures, "a shipped tick resets the streak")
		assert.Equal(t, now.Add(time.Hour), snap.LastShip)
		assert.Equal(t, int64(2), snap.FilesShippedLifetime)
	})

	t.Run("partial-success busy tick is non-systemic yet surfaces the failing file", func(t *testing.T) {
		tr := NewUploadHealthTracker()
		// Prime a streak so the reset (non-systemic classification) is observable.
		tr.Record(Summary{FilesUploaded: 0, Files: []FileSummary{{Err: "x"}}}, boom, now)
		snap := tr.Record(Summary{
			FilesUploaded: 2,
			Files:         []FileSummary{{Session: "ok"}, {Session: "stuck", Err: "over cap"}},
		}, errors.New("over cap"), now.Add(time.Minute))

		assert.Equal(t, 0, snap.ConsecutiveFailures, "a tick that shipped >0 is not a systemic failure")
		assert.Equal(t, 1, snap.FilesFailedLastTick, "the degraded axis still counts the failing file")
		assert.Equal(t, "over cap", snap.LastError, "LastError is set even on a partial-success tick")
		assert.False(t, snap.LastShip.IsZero(), "a partial success still advances LastShip")
	})

	t.Run("Snapshot returns an independent value copy", func(t *testing.T) {
		tr := NewUploadHealthTracker()
		tr.Record(Summary{FilesUploaded: 1}, nil, now)

		snap := tr.Snapshot()
		snap.ConsecutiveFailures = 99
		snap.LastError = "mutated"

		again := tr.Snapshot()
		assert.Equal(t, 0, again.ConsecutiveFailures, "mutating the returned copy does not touch the tracker")
		assert.Empty(t, again.LastError)
	})
}
