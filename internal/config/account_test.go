// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wantAccountComment is the mandated two-line comment, spelled out here rather
// than referenced from the const so a change to the written block is caught.
const wantAccountComment = "# The Fulminate account this machine's cloud calls are routed to.\n" +
	"# Written by `knowledge login` and changed by `knowledge account use`."

// commentedConfig is a stand-in for the real starter: leading comments, a
// commented-out key, and two table sections.
const commentedConfig = `# knowledge config — edit freely.
# Comments in this file must survive every write.

# health_probe_interval = "10m"

[default]
provider = "anthropic"
model = "claude-haiku-5"

# The summarizer section overrides [default].
[summarizer]
model = "claude-opus-4-7"
`

func writeTempConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	// os.WriteFile respects umask on create; force the exact mode.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod seed config: %v", err)
	}
	return path
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestWriteSelectedAccountID_PreservesCommentsAndMode covers the four
// obligations of the upsert on an existing, commented file: it inserts, it
// replaces in place on a re-run, every original byte survives, and the file
// mode is preserved rather than hard-coded.
func TestWriteSelectedAccountID_PreservesCommentsAndMode(t *testing.T) {
	const idOne = "acct_01AAAAAAAAAAAAAAAAAAAAAAAA"
	const idTwo = "acct_01BBBBBBBBBBBBBBBBBBBBBBBB"

	path := writeTempConfig(t, commentedConfig, 0o600)

	if err := WriteSelectedAccountID(path, idOne); err != nil {
		t.Fatalf("WriteSelectedAccountID(insert): %v", err)
	}
	afterInsert := readFileString(t, path)

	// Byte-for-byte preservation: deleting exactly the inserted block from the
	// result must yield the original file, byte for byte.
	// Inserted before the first table header: a blank line, the two comment
	// lines, then the assignment — three lines, so three newlines.
	insertedBlock := "\n" + wantAccountComment + "\n" + accountKey + ` = "` + idOne + `"` + "\n"
	if !strings.Contains(afterInsert, insertedBlock) {
		t.Fatalf("inserted block not found verbatim; file:\n%s", afterInsert)
	}
	if restored := strings.Replace(afterInsert, insertedBlock, "", 1); restored != commentedConfig {
		t.Errorf("insert did not preserve the original bytes.\n got: %q\nwant: %q", restored, commentedConfig)
	}

	// Re-run: replace in place. The only byte difference from the first write
	// must be the id itself — no second comment block, no moved lines.
	if err := WriteSelectedAccountID(path, idTwo); err != nil {
		t.Fatalf("WriteSelectedAccountID(replace): %v", err)
	}
	afterReplace := readFileString(t, path)
	if want := strings.Replace(afterInsert, idOne, idTwo, 1); afterReplace != want {
		t.Errorf("re-run was not an in-place replace.\n got: %q\nwant: %q", afterReplace, want)
	}
	if n := strings.Count(afterReplace, accountKey+" ="); n != 1 {
		t.Errorf("account key assigned %d times, want exactly 1:\n%s", n, afterReplace)
	}
	if n := strings.Count(afterReplace, wantAccountComment); n != 1 {
		t.Errorf("comment block appears %d times, want exactly 1", n)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := st.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %04o, want 0600", got)
	}

	// Known-positive control for the mode assertion: a file seeded at a
	// DIFFERENT mode keeps that mode, proving 0600 above was read from the
	// file rather than hard-coded by the writer.
	other := writeTempConfig(t, commentedConfig, 0o640)
	if err := WriteSelectedAccountID(other, idOne); err != nil {
		t.Fatalf("WriteSelectedAccountID(0640 file): %v", err)
	}
	st2, err := os.Stat(other)
	if err != nil {
		t.Fatalf("stat 0640 file: %v", err)
	}
	if got := st2.Mode().Perm(); got != 0o640 {
		t.Errorf("0640 file: mode = %04o, want 0640 (mode is preserved, not hard-coded)", got)
	}
}

// TestWriteSelectedAccountID_RoundTripsAtTopLevel proves the written key binds
// at top level: it parses back to the same id, and it is positioned before the
// first table header (a bare key after a header would bind to that table).
func TestWriteSelectedAccountID_RoundTripsAtTopLevel(t *testing.T) {
	const id = "acct_01CCCCCCCCCCCCCCCCCCCCCCCC"
	path := writeTempConfig(t, commentedConfig, 0o600)

	if err := WriteSelectedAccountID(path, id); err != nil {
		t.Fatalf("WriteSelectedAccountID: %v", err)
	}
	body := readFileString(t, path)

	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse(written config): %v", err)
	}
	if cfg.FulminateAccountID != id {
		t.Errorf("FulminateAccountID = %q, want %q", cfg.FulminateAccountID, id)
	}
	// The rest of the document still parses as before.
	if cfg.Default.Provider != ProviderAnthropic {
		t.Errorf("Default.Provider = %q, want %q", cfg.Default.Provider, ProviderAnthropic)
	}

	keyAt := strings.Index(body, accountKey+" =")
	headerAt := strings.Index(body, "[default]")
	if keyAt < 0 || headerAt < 0 {
		t.Fatalf("key at %d, [default] at %d in:\n%s", keyAt, headerAt, body)
	}
	if keyAt > headerAt {
		t.Errorf("key written after the first table header (key %d > header %d) — it would bind to that table:\n%s", keyAt, headerAt, body)
	}

	// A file with no table header at all also round-trips.
	bare := writeTempConfig(t, "# only comments here\n", 0o600)
	if err := WriteSelectedAccountID(bare, id); err != nil {
		t.Fatalf("WriteSelectedAccountID(headerless): %v", err)
	}
	cfg2, err := Parse([]byte(readFileString(t, bare)))
	if err != nil {
		t.Fatalf("Parse(headerless): %v", err)
	}
	if cfg2.FulminateAccountID != id {
		t.Errorf("headerless: FulminateAccountID = %q, want %q", cfg2.FulminateAccountID, id)
	}

	// An absent file is created carrying only the block.
	missing := filepath.Join(t.TempDir(), "nested", "config")
	if err := WriteSelectedAccountID(missing, id); err != nil {
		t.Fatalf("WriteSelectedAccountID(absent file): %v", err)
	}
	cfg3, err := Parse([]byte(readFileString(t, missing)))
	if err != nil {
		t.Fatalf("Parse(created config): %v", err)
	}
	if cfg3.FulminateAccountID != id {
		t.Errorf("created file: FulminateAccountID = %q, want %q", cfg3.FulminateAccountID, id)
	}
}

// TestReadSelectedAccountID_NoSideEffects pins the read contract: an absent
// file is ("", nil), a stored id comes back verbatim, and no read installs the
// process config singleton (which Load, deliberately not used, would do).
func TestReadSelectedAccountID_NoSideEffects(t *testing.T) {
	cleanup := SetForTest(nil)
	defer cleanup()

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	got, err := ReadSelectedAccountID(missing)
	if err != nil {
		t.Fatalf("ReadSelectedAccountID(absent): unexpected error %v", err)
	}
	if got != "" {
		t.Errorf("ReadSelectedAccountID(absent) = %q, want empty", got)
	}

	// Known-positive control: the same function DOES return a stored id, so
	// the empty answer above is a real absence and not a dead reader.
	const id = "acct_01DDDDDDDDDDDDDDDDDDDDDDDD"
	path := writeTempConfig(t, commentedConfig, 0o600)
	if err := WriteSelectedAccountID(path, id); err != nil {
		t.Fatalf("seed selection: %v", err)
	}
	got, err = ReadSelectedAccountID(path)
	if err != nil {
		t.Fatalf("ReadSelectedAccountID(present): %v", err)
	}
	if got != id {
		t.Errorf("ReadSelectedAccountID(present) = %q, want %q", got, id)
	}

	// The singleton must still be unset — Active() panics when nothing has
	// installed a config. A read that had called Load would have installed one.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("ReadSelectedAccountID installed the config singleton; it must be side-effect-free")
			}
		}()
		_ = Active()
	}()
}

// TestWriteSelectedAccountID_RefusesEmpty pins permanence: the selection can be
// changed but never cleared. An empty or whitespace-only id is refused and the
// file is left byte-for-byte unchanged.
func TestWriteSelectedAccountID_RefusesEmpty(t *testing.T) {
	const id = "acct_01EEEEEEEEEEEEEEEEEEEEEEEE"

	for _, empty := range []string{"", " ", "\t", "\n", "   \t\n "} {
		path := writeTempConfig(t, commentedConfig, 0o600)
		if err := WriteSelectedAccountID(path, id); err != nil {
			t.Fatalf("seed selection: %v", err)
		}
		before := readFileString(t, path)

		err := WriteSelectedAccountID(path, empty)
		if err == nil {
			t.Errorf("WriteSelectedAccountID(%q): want error, got nil", empty)
		}
		if after := readFileString(t, path); after != before {
			t.Errorf("WriteSelectedAccountID(%q) modified the file.\n got: %q\nwant: %q", empty, after, before)
		}
		// The stored selection is still readable — nothing was cleared.
		got, readErr := ReadSelectedAccountID(path)
		if readErr != nil {
			t.Fatalf("ReadSelectedAccountID after refusal: %v", readErr)
		}
		if got != id {
			t.Errorf("after refusing %q, selection = %q, want %q", empty, got, id)
		}
	}

	// Known-positive control: a non-empty id DOES change the file, so the
	// unchanged-file assertions above are not vacuous.
	path := writeTempConfig(t, commentedConfig, 0o600)
	before := readFileString(t, path)
	if err := WriteSelectedAccountID(path, id); err != nil {
		t.Fatalf("WriteSelectedAccountID(valid): %v", err)
	}
	if after := readFileString(t, path); after == before {
		t.Error("a valid id did not change the file — the unchanged-file assertions would be vacuous")
	}
}
