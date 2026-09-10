// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestTranscriptUploadCadence_ReadsTheDeclaredConstants pins the daemon's upload
// cadence at its declaration.
//
// The defect this catches is a silent cadence change: the interval is consumed
// exactly once (maybeStartTranscriptUpload hands it to runTranscriptUploadLoop) and
// the loop test injects its OWN 5ms interval, so before this test nothing whatsoever
// read the constant and any value at all would have kept the suite green.
//
// The boot delay is asserted in the same run for the opposite reason: it is
// deliberately NOT part of the cadence change, and an out-of-scope constant is only
// observably unchanged if something reads it.
func TestTranscriptUploadCadence_ReadsTheDeclaredConstants(t *testing.T) {
	assert.Equal(t, 10*time.Minute, transcriptUploadInterval,
		"the background upload ticker fires every 10 minutes")
	assert.Equal(t, 1*time.Minute, transcriptUploadBootDelay,
		"the boot delay is out of scope and stays at one minute")
}
