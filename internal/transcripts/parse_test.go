// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"path/filepath"
	"testing"
)

func TestParseDispatch(t *testing.T) {
	dir := t.TempDir()

	claudePath := filepath.Join(dir, "sess.jsonl")
	mustWrite(t, claudePath, claudeFixture)
	codexPath := filepath.Join(dir, "rollout-x.jsonl")
	mustWrite(t, codexPath, codexFixture)

	t.Run("routes claude", func(t *testing.T) {
		rows, err := Parse(Entry{Path: claudePath, Source: SourceClaude})
		if err != nil {
			t.Fatalf("Parse claude: %v", err)
		}
		if len(rows) == 0 || rows[0].Source != SourceClaude {
			t.Fatalf("expected claude rows, got %+v", rows)
		}
	})

	t.Run("routes codex", func(t *testing.T) {
		rows, err := Parse(Entry{Path: codexPath, Source: SourceCodex})
		if err != nil {
			t.Fatalf("Parse codex: %v", err)
		}
		if len(rows) == 0 || rows[0].Source != SourceCodex {
			t.Fatalf("expected codex rows, got %+v", rows)
		}
	})

	t.Run("errors on unknown source", func(t *testing.T) {
		if _, err := Parse(Entry{Path: claudePath, Source: Source("mystery")}); err == nil {
			t.Fatal("expected error for unknown source, got nil")
		}
	})
}
