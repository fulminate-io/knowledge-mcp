// SPDX-License-Identifier: Apache-2.0

package codexcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/llm"
)

// writeFakeCodexBin writes an executable POSIX shell script to a tempdir
// and returns its absolute path. Tests that need to exercise the subprocess
// path inject this via Config.CLIBin so the resolver returns the stub
// instead of resolving a real `codex` binary on the developer's PATH.
//
// scriptBody is appended verbatim to a `#!/bin/sh` shebang. Tests pass
// transcripts that mimic codex's `exec --json` JSONL output. Use single
// quotes around heredoc bodies to avoid shell expansion of the canned JSON.
//
// 0o700 because the file MUST be executable; gosec flags >0o600 on
// os.WriteFile so we annotate the call site.
func writeFakeCodexBin(t *testing.T, scriptBody string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake codex bin relies on POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-codex")
	content := "#!/bin/sh\n" + scriptBody + "\n"
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write fake codex bin: %v", err)
	}
	return path
}

// recordingFakeCodex writes a fake codex that captures argv (one per line)
// and stdin to two separate files inside the returned recording directory.
// The recording dir is the parent of the binary so tests can read both.
//
// stdoutBody is what the fake emits to its stdout — usually a canned JSONL
// transcript representing a successful turn.
//
// Returns: binary path, argvFile path, stdinFile path.
func recordingFakeCodex(t *testing.T, stdoutBody string) (string, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("recording fake codex relies on POSIX shell")
	}
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	stdinFile := filepath.Join(dir, "stdin")
	binPath := filepath.Join(dir, "fake-codex")

	// Encode the canned stdout as a base64 blob so embedded newlines and
	// quotes pass cleanly through the shell. The script decodes it via the
	// system `base64` tool at runtime.
	encoded := encodeStdout(stdoutBody)
	script := fmt.Sprintf(`#!/bin/sh
for a in "$@"; do
  printf '%%s\n' "$a" >> %q
done
cat > %q
printf '%%s' %q | base64 -d
`, argvFile, stdinFile, encoded)
	if err := os.WriteFile(binPath, []byte(script), 0o700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write recording fake codex: %v", err)
	}
	return binPath, argvFile, stdinFile
}

// encodeStdout base64-encodes a body for embedding in the recording fake's
// shell script. The script then decodes via the system `base64` tool, so
// tests don't need a separate helper binary on PATH.
//
// Implemented inline (rather than via encoding/base64) so the test code
// stays self-contained in this single file when read in isolation. The
// alphabet is the standard RFC 4648 set with `=` padding.
func encodeStdout(body string) string {
	const tab = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	src := []byte(body)
	var sb strings.Builder
	for i := 0; i < len(src); i += 3 {
		var b [3]byte
		n := copy(b[:], src[i:])
		sb.WriteByte(tab[b[0]>>2])
		sb.WriteByte(tab[((b[0]&0x03)<<4)|(b[1]>>4)])
		if n > 1 {
			sb.WriteByte(tab[((b[1]&0x0f)<<2)|(b[2]>>6)])
		} else {
			sb.WriteByte('=')
		}
		if n > 2 {
			sb.WriteByte(tab[b[2]&0x3f])
		} else {
			sb.WriteByte('=')
		}
	}
	return sb.String()
}

// successfulTranscript is the canned JSONL stdout a successful codex exec
// turn would emit. Pinned in tests so the parser can verify field handling
// without depending on a real codex install.
const successfulTranscript = `{"type":"thread.started","thread_id":"019deadc-aaaa-bbbb-cccc-dddddddddddd"}
{"type":"turn.started"}
{"type":"item.completed","item":{"item_type":"agent_message","text":"hello back"}}
{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":5}}
`

// mustNewService is a test helper that constructs a Service with a
// fake-codex binary path. Fails the test on any error so callers can
// chain straight into Generate.
func mustNewService(t *testing.T, bin string, defaultModel llm.Model) *Service {
	t.Helper()
	cfg := &llm.Config{
		Provider: llm.ProviderCodexCLI,
		CLIBin:   bin,
		Model:    defaultModel,
	}
	c, err := llm.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	svc, ok := c.(*Service)
	if !ok {
		t.Fatalf("expected *Service, got %T", c)
	}
	return svc
}

// readArgv reads the per-line argv recorder file and returns the list.
// The recording fake codex script writes one arg per line via printf '%s\n'.
func readArgv(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read argv file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// contains reports whether xs has x somewhere in it.
func contains(xs []string, x string) bool {
	return slices.Contains(xs, x)
}

// indexOf returns the index of x in xs, or -1 if absent.
func indexOf(xs []string, x string) int {
	return slices.Index(xs, x)
}
