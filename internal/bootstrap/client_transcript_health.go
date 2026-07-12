// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "github.com/fulminate-io/knowledge-mcp/internal/transcriptsync"

// TranscriptUploadHealth returns a snapshot of the background transcript-upload loop's
// health (success/failure counters, last-transport-ok / last-ship timestamps, the
// consecutive-systemic-failure streak, and the per-file degraded signal). It satisfies
// the optional tools.transcriptUploadHealther interface the manage(status) surface reads
// so an operator can see a persistent upload failure instead of it shipping nothing
// invisibly.
//
// ok=false when transcriptHealth is nil (test-built clients, or a daemon that never
// reached the loop-spawn stage); callers render that case as absent rather than as a
// healthy zero snapshot, mirroring PipelineMetrics.
func (c *client) TranscriptUploadHealth() (transcriptsync.UploadHealth, bool) {
	if c.transcriptHealth == nil {
		return transcriptsync.UploadHealth{}, false
	}
	return c.transcriptHealth.Snapshot(), true
}
