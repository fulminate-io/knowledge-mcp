// SPDX-License-Identifier: Apache-2.0

package clientver

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetSelfHandle drops any held descriptor so each test opens its own.
func resetSelfHandle(t *testing.T) {
	t.Helper()
	selfMu.Lock()
	if selfFile != nil {
		_ = selfFile.Close()
		selfFile = nil
	}
	selfMu.Unlock()
	t.Cleanup(func() {
		selfMu.Lock()
		if selfFile != nil {
			_ = selfFile.Close()
			selfFile = nil
		}
		selfMu.Unlock()
	})
}

// withStubbedSelf points openSelf at path for the duration of the test,
// mirroring the getExecutable stub seam the daemon lifecycle tests use.
func withStubbedSelf(t *testing.T, path string) {
	t.Helper()
	prev := openSelf
	openSelf = func() (*os.File, error) { return os.Open(path) } //nolint:gosec // test fixture path
	t.Cleanup(func() { openSelf = prev })
}

// expectedDigest is the INDEPENDENT expectation: computed here from the fixture
// bytes the test itself wrote, never from the implementation's own output.
func expectedDigest(nonce, fileBytes []byte, offset, length int64) string {
	h := sha256.New()
	h.Write(nonce)
	h.Write(fileBytes[offset : offset+length])
	return hex.EncodeToString(h.Sum(nil))
}

// TestSelfExecutableDigest_SurvivesBinarySwap performs the exact replacement
// the installer performs on unix — os.Rename of a sibling temp file OVER the
// destination path, with NO unlink, because the installer removes the
// destination first only on windows and refuses client self-update there
// outright — and asserts the held descriptor still answers with the ORIGINAL
// bytes.
//
// The RE-OPEN CONTROL is what makes the pass meaningful. Without it a green
// would be equally consistent with a swap that never took effect.
func TestSelfExecutableDigest_SurvivesBinarySwap(t *testing.T) {
	resetSelfHandle(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "knowledge")
	oldBytes := []byte(strings.Repeat("OLD-BINARY-BYTES-AAAA", 64))
	newBytes := []byte(strings.Repeat("NEW-BINARY-BYTES-BBBB", 64))
	if err := os.WriteFile(target, oldBytes, 0o755); err != nil { //nolint:gosec // fixture binary needs the executable bit
		t.Fatalf("write fixture: %v", err)
	}

	withStubbedSelf(t, target)
	if err := OpenSelf(); err != nil {
		t.Fatalf("OpenSelf: %v", err)
	}

	nonce := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01, 0x02, 0x03}
	const offset, length = int64(16), int64(128)

	before, err := AnswerChallenge(nonce, offset, length)
	if err != nil {
		t.Fatalf("AnswerChallenge before swap: %v", err)
	}
	if want := expectedDigest(nonce, oldBytes, offset, length); before != want {
		t.Fatalf("digest before swap = %s, want %s (nonce bytes first, then the range, no separator)", before, want)
	}

	// THE SWAP, statement for statement as the installer performs it on unix.
	tmp := filepath.Join(dir, "knowledge-install-tmp")
	if err := os.WriteFile(tmp, newBytes, 0o755); err != nil { //nolint:gosec // fixture binary needs the executable bit
		t.Fatalf("write replacement: %v", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatalf("rename over the running binary: %v", err)
	}

	// THE CONTROL: a re-open of the SAME PATH reads the NEW bytes, proving the
	// swap actually took effect in this run.
	reopened, err := os.Open(target) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatalf("re-open target: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	control := make([]byte, length)
	if _, err := reopened.ReadAt(control, offset); err != nil {
		t.Fatalf("read via re-opened path: %v", err)
	}
	h := sha256.New()
	h.Write(nonce)
	h.Write(control)
	reopenDigest := hex.EncodeToString(h.Sum(nil))
	if want := expectedDigest(nonce, newBytes, offset, length); reopenDigest != want {
		t.Fatalf("the control did not observe the new bytes; the swap did not take effect, so the assertion below proves nothing")
	}
	if reopenDigest == before {
		t.Fatalf("the fixture's old and new bytes hash identically over this range, so the test cannot distinguish them")
	}

	// THE PROPERTY: the held descriptor still serves the bytes this process was
	// launched from.
	after, err := AnswerChallenge(nonce, offset, length)
	if err != nil {
		t.Fatalf("AnswerChallenge after swap: %v", err)
	}
	if after != before {
		t.Errorf("the possession answer changed across a binary swap: got %s, want the original %s — the implementation is re-opening the path instead of reading the held descriptor, so after a self-update it would prove the new binary while claiming the old version", after, before)
	}
}

// TestAnswerChallenge_RefusesRatherThanClamping pins the two range bounds. A
// clamped or short read yields a wrong-but-well-formed digest the verifier
// cannot distinguish from a spoof, so both must be errors.
func TestAnswerChallenge_RefusesRatherThanClamping(t *testing.T) {
	resetSelfHandle(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "knowledge")
	body := []byte(strings.Repeat("z", 4096))
	if err := os.WriteFile(target, body, 0o755); err != nil { //nolint:gosec // fixture binary needs the executable bit
		t.Fatalf("write fixture: %v", err)
	}
	withStubbedSelf(t, target)
	if err := OpenSelf(); err != nil {
		t.Fatalf("OpenSelf: %v", err)
	}
	nonce := []byte{0x01}

	if _, err := AnswerChallenge(nonce, 0, maxChallengeLength+1); err == nil {
		t.Errorf("a length above the ceiling must be refused, not truncated")
	} else if !strings.Contains(err.Error(), "ceiling") {
		t.Errorf("the ceiling refusal should name the ceiling: %v", err)
	}
	if _, err := AnswerChallenge(nonce, 4000, 1000); err == nil {
		t.Errorf("a range running past the end of the file must be refused, not short-read")
	}
	if _, err := AnswerChallenge(nonce, -1, 10); err == nil {
		t.Errorf("a negative offset must be refused")
	}

	// KNOWN-POSITIVE, same run: a range INSIDE both bounds succeeds, so the
	// refusals above are properties of the bounds rather than of a function
	// that always errors.
	got, err := AnswerChallenge(nonce, 0, 4096)
	if err != nil {
		t.Fatalf("the control failed: an in-bounds range must succeed: %v", err)
	}
	if want := expectedDigest(nonce, body, 0, 4096); got != want {
		t.Errorf("in-bounds digest = %s, want %s", got, want)
	}
}

// TestAnswerChallenge_RefusesBeforeTheHandleIsOpen pins the unwired-OpenSelf
// posture: it is an explicit error naming the condition, never a zero digest.
func TestAnswerChallenge_RefusesBeforeTheHandleIsOpen(t *testing.T) {
	resetSelfHandle(t)
	if _, err := AnswerChallenge([]byte{0x01}, 0, 1); err == nil {
		t.Fatalf("answering with no handle open must be an error")
	}
}

// TestOpenSelf_IsIdempotent proves a second call does not replace or leak the
// held descriptor, so a future second call site is harmless.
func TestOpenSelf_IsIdempotent(t *testing.T) {
	resetSelfHandle(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "knowledge")
	if err := os.WriteFile(target, []byte("abcdefghij"), 0o755); err != nil { //nolint:gosec // fixture binary needs the executable bit
		t.Fatalf("write fixture: %v", err)
	}
	withStubbedSelf(t, target)

	if err := OpenSelf(); err != nil {
		t.Fatalf("first OpenSelf: %v", err)
	}
	selfMu.Lock()
	first := selfFile
	selfMu.Unlock()

	if err := OpenSelf(); err != nil {
		t.Fatalf("second OpenSelf: %v", err)
	}
	selfMu.Lock()
	second := selfFile
	selfMu.Unlock()

	if first != second {
		t.Errorf("a second OpenSelf replaced the held descriptor, leaking the first")
	}
}
