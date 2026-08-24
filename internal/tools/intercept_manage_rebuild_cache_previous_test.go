// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManageRebuildCache_RendersPreviousOutcome pins the operator-facing half of
// the rebuild's observability: the started-ack reports what the PREVIOUS run
// did, so an operator who cannot read the server's stderr still learns whether
// the last rebuild completed, failed, or never happened.
//
// The three subtests are the three payload shapes a real server produces, and
// each is kept distinct because collapsing any two of them is a wrong answer
// delivered confidently.
func TestManageRebuildCache_RendersPreviousOutcome(t *testing.T) {
	t.Run("empty_result_json_renders_todays_ack", func(t *testing.T) {
		// A server that sends no payload is a legitimate state, not an error, and
		// the ack must be exactly what it was before this feature existed.
		ix := &fakeIndexer{}
		handled, res := manageCall(t, ix, `{"operation":"rebuild_cache","graph":"code","name":"myrepo"}`)
		require.True(t, handled)
		require.False(t, res.IsError, "rebuild_cache: %s", toolResultText(res))

		body := toolResultText(res)
		assert.Contains(t, body, "rebuild_cache started for code/myrepo")
		assert.NotContains(t, body, "Previous rebuild:")
		assert.NotContains(t, body, "No previous rebuild outcome")
		assert.NotContains(t, body, "WARNING")
	})

	t.Run("previous_complete_is_rendered", func(t *testing.T) {
		ix := &fakeIndexer{resultJSON: []byte(`{"operation":"rebuild_cache","ok":true,"started":true,` +
			`"previous":{"present":true,"outcome":{"state":"complete","derived":2887,` +
			`"started_at":"2026-08-23T09:00:00Z","finished_at":"2026-08-23T09:04:00Z"}}}`)}
		handled, res := manageCall(t, ix, `{"operation":"rebuild_cache","graph":"code","name":"myrepo"}`)
		require.True(t, handled)
		require.False(t, res.IsError, "rebuild_cache: %s", toolResultText(res))

		body := toolResultText(res)
		assert.Contains(t, body, "Previous rebuild: complete")
		assert.Contains(t, body, "2887 entries derived",
			"the derived count is the whole point — an outcome without it does not distinguish a real rebuild from an empty one")
		assert.Contains(t, body, "2026-08-23T09:04:00Z")

		// KNOWN-NEGATIVE CONTROL in the same run: the SAME renderer must produce a
		// different, explicit sentence when there is no recorded run. Without this,
		// the assertions above could pass against a renderer that appends its text
		// unconditionally.
		ixNone := &fakeIndexer{resultJSON: []byte(`{"operation":"rebuild_cache","ok":true,"started":true,` +
			`"previous":{"present":false}}`)}
		_, resNone := manageCall(t, ixNone, `{"operation":"rebuild_cache","graph":"code","name":"myrepo"}`)
		noneBody := toolResultText(resNone)
		assert.Contains(t, noneBody, "No previous rebuild outcome is recorded")
		assert.NotContains(t, noneBody, "Previous rebuild: complete")
	})

	t.Run("unparseable_result_json_is_loud", func(t *testing.T) {
		// A malformed payload is a real condition. Rendering silence over it would
		// reproduce the exact defect this reporting exists to close.
		ix := &fakeIndexer{resultJSON: []byte(`{"operation":"rebuild_cache",`)}
		handled, res := manageCall(t, ix, `{"operation":"rebuild_cache","graph":"code","name":"myrepo"}`)
		require.True(t, handled)
		require.False(t, res.IsError,
			"a malformed ack must NOT fail the op — the rebuild was started, and saying otherwise would be false")

		body := toolResultText(res)
		assert.Contains(t, body, "WARNING")
		assert.Contains(t, body, "could not be parsed")
		assert.Contains(t, body, "rebuild_cache started for code/myrepo",
			"the started-ack survives alongside the warning")
	})
}
