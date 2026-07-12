// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// TestTranscriptUploadCmd_Help_ExitsZero asserts `--help` prints usage and returns
// nil (exit 0) without reaching the transport builder.
func TestTranscriptUploadCmd_Help_ExitsZero(t *testing.T) {
	require.NoError(t, TranscriptUploadCmd([]string{"--help"}))
}

// TestRunTranscriptUpload_DryRun_FullyOffline is the T3 offline-dry-run guard:
// `--dry-run` enumerates + parses + reports per file WITHOUT touching the keychain
// (buildSyncTransportFn must never be called) or writing a watermark.
func TestRunTranscriptUpload_DryRun_FullyOffline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed one real claude transcript so the dry-run has something to parse.
	projDir := filepath.Join(home, ".claude", "projects", "proj1")
	require.NoError(t, os.MkdirAll(projDir, 0o750))
	rec := `{"type":"assistant","uuid":"u1","timestamp":"2026-06-01T00:00:00Z","cwd":"/work","gitBranch":"main","version":"1.0","sessionId":"sess-uuid","message":{"role":"assistant","model":"claude-opus","usage":{"input_tokens":100,"output_tokens":50}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "sess-uuid.jsonl"), []byte(rec), 0o600))

	// The dry-run path must NEVER reach the transport builder (no keychain access).
	prev := buildSyncTransportFn
	buildSyncTransportFn = func() (*auth.Transport, error) {
		t.Fatal("dry-run must not access the keychain / build a transport")
		return nil, nil
	}
	t.Cleanup(func() { buildSyncTransportFn = prev })

	var buf bytes.Buffer
	require.NoError(t, runTranscriptUpload(context.Background(), &buf, false, 0, 0, true))

	out := buf.String()
	assert.Contains(t, out, "DRY RUN", "offline preview is labeled")
	assert.Contains(t, out, "claude", "the claude source is reported")
	assert.Contains(t, out, "sess-uuid", "the derived session is reported")
	assert.Contains(t, out, "1 row(s)", "the parsed session row count is reported per file")

	// Dry-run writes no watermark.
	_, statErr := os.Stat(filepath.Join(home, ".knowledge", "transcript-watermarks.json"))
	assert.True(t, os.IsNotExist(statErr), "dry-run writes no watermark")
}

// TestRunTranscriptUploadOnce_HonorsInjectedTransportFactory proves the daemon
// (non-dry-run) path resolves its transport through the INJECTED factory, not
// the env+keychain default: a factory that fails with a sentinel error surfaces
// as the returned error, because execTranscriptUpload builds the transport
// before transcriptsync.Run. This is the seam the daemon uses to hand the loop
// its shared cloud token source.
func TestRunTranscriptUploadOnce_HonorsInjectedTransportFactory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	sentinel := errors.New("injected transport factory failed")
	_, err := RunTranscriptUploadOnce(context.Background(), func() (*auth.Transport, error) {
		return nil, sentinel
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "the daemon path must surface the injected factory's error")
}
