// SPDX-License-Identifier: Apache-2.0

package transcriptsync

import (
	"context"
	"fmt"
	"io"

	"github.com/fulminate-io/knowledge-mcp/internal/transcripts"
)

// ControlTransport is the small-control-request seam the engine drives for the
// presign / confirm / consent POSTs. It is the exact method subset pushGraph
// already uses, so the production *auth.Transport
// (cmd/knowledge/internal/auth/sync_transport.go) satisfies it as-is with no
// adapter — every call is a Bearer-authenticated POST to /v1/sync/<path> with
// 401-refresh-retry handled inside the transport. Keeping the engine to this
// interface (rather than the concrete transport) keeps it auth-free and
// unit-testable with a fake.
type ControlTransport interface {
	SyncControlJSON(ctx context.Context, path string, body []byte) ([]byte, error)
}

// CorpusEnumerator surfaces the local transcript corpus as flat file descriptors.
// The cli wraps KN-1's transcripts.Enumerate in an adapter satisfying this seam
// (deriving Session per file); the engine stays decoupled from the corpus walk so
// tests inject a fixed file list.
type CorpusEnumerator interface {
	Enumerate() ([]TranscriptFile, error)
}

// TranscriptFile is one enumerated transcript: its absolute path, the CLI that
// wrote it, the session id that namespaces its uploaded parts, and the size
// captured at walk. The engine re-stats Path at upload time for the LIVE size +
// mod time (the enumerator size is a snapshot; the live mod time drives reseed
// detection), so there is deliberately no Mtime field here. Source is the bare
// CLI string ("claude" / "codex"); Session is the per-file identity AG-1 uses in
// the part-key namespace (== the parsed Row.SessionID).
type TranscriptFile struct {
	Path    string
	Source  string
	Session string
	Size    int64
}

// ParseFunc is the whole-file parse seam. It takes the bare source string and a
// reader over the ENTIRE file and returns the normalized Rows (each carrying its
// transient SourceOffset). It defaults to ParseTranscript; tests swap in a fake
// returning canned Rows. A reader (not a path) keeps the engine testable without
// touching disk and lets the caller open the file once for both Stat and Parse.
type ParseFunc func(source string, r io.Reader) ([]transcripts.Row, error)

// ParseTranscript is the default ParseFunc: it dispatches on the CLI source to
// KN-1's exported reader-based parsers (client-side parse reuse — the
// engine delegates parsing to KN-1, never re-implements it). Both parsers stream
// the reader line-by-line, so passing the whole opened file reconstructs the
// stateful Codex scan (session_meta on line 1, per-turn model) without holding
// the raw bytes in memory.
func ParseTranscript(source string, r io.Reader) ([]transcripts.Row, error) {
	switch transcripts.Source(source) {
	case transcripts.SourceClaude:
		return transcripts.ParseClaude(r)
	case transcripts.SourceCodex:
		return transcripts.ParseCodex(r)
	default:
		return nil, fmt.Errorf("transcriptsync: unknown transcript source %q", source)
	}
}
