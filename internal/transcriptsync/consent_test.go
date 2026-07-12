// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConsentEnabled covers the three gate outcomes:
//   - disabled flag → (false, nil): skip the batch.
//   - transport error → (false, err): skip-and-retry.
//   - enabled flag → (true, nil): proceed.
func TestConsentEnabled(t *testing.T) {
	t.Run("disabled returns false,nil", func(t *testing.T) {
		backend := newFakeTranscriptBackend(t)
		backend.consentEnabledFlag = false

		enabled, err := consentEnabled(context.Background(), backend)
		require.NoError(t, err)
		assert.False(t, enabled, "disabled flag → (false, nil)")
		assert.Equal(t, 1, backend.consentCalls, "consent fetched once")
	})

	t.Run("fetch error returns false,err", func(t *testing.T) {
		backend := newFakeTranscriptBackend(t)
		backend.consentErr = true

		enabled, err := consentEnabled(context.Background(), backend)
		require.Error(t, err, "an HTTP/transport error surfaces for skip-and-retry")
		assert.False(t, enabled, "an errored fetch never reports enabled")
	})

	t.Run("enabled returns true,nil", func(t *testing.T) {
		backend := newFakeTranscriptBackend(t)
		backend.consentEnabledFlag = true

		enabled, err := consentEnabled(context.Background(), backend)
		require.NoError(t, err)
		assert.True(t, enabled, "enabled flag → (true, nil)")
	})
}
