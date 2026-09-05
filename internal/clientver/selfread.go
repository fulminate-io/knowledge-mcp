// SPDX-License-Identifier: Apache-2.0

package clientver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
)

// maxChallengeLength bounds the byte range this client will read out of its own
// executable to answer a possession challenge, at 1 MiB.
//
// It is a CROSS-REPO CONTRACT, not a local convenience: the verifier records
// the same ceiling on its own side, with a compile-time guard that the range it
// asks for never exceeds it. The ceiling exists because the verifier chooses
// the range and this client performs the read — an unbounded length would let
// the far side dictate an arbitrarily large read here. Today's challenge slice
// sits an order of magnitude inside it, so the constraint binds nothing; it is
// recorded because a later widening of that slice would otherwise break every
// client with NO compile-time tell on either side of the repo boundary.
//
// A range above the ceiling is refused BY ERROR and never truncated. See
// AnswerChallenge for why a short read is worse than a refusal.
const maxChallengeLength = 1 << 20

// openSelf is a stubbable alias for opening the running executable, mirroring
// the getExecutable seam the daemon lifecycle code uses so tests can point the
// self-read at a fixture binary without re-execing the test binary.
var openSelf = func() (*os.File, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return os.Open(path) //nolint:gosec // deliberately opens this process's own executable
}

var (
	selfMu   sync.Mutex
	selfFile *os.File
)

// OpenSelf opens the running executable ONCE and holds the descriptor for the
// life of the process. Calling it again while a handle is held is a no-op, so a
// second call site cannot leak a descriptor.
//
// WHY THE DESCRIPTOR IS HELD RATHER THAN THE PATH REOPENED PER CHALLENGE. The
// client can replace its own binary in place while running: the installer
// writes a sibling temp file and renames it OVER the destination path on unix
// (it unlinks the destination first only on windows, where client self-update
// is refused outright). A POSIX rename-over swaps the directory entry while
// leaving the original inode alive for any descriptor already open on it. So a
// held handle keeps serving the bytes this process was actually LAUNCHED from
// — which are the bytes matching the version it claims — while a re-open of the
// same path after an update would read the NEW binary. Answering from a re-open
// would therefore compute a proof over one binary while claiming another
// binary's version, the proof would fail, no verification record would be
// written, and every subsequent cloud call would be refused until restart.
//
// This is the single most consequential decision in the possession design and
// the one a reasonable simplification would undo.
func OpenSelf() error {
	selfMu.Lock()
	defer selfMu.Unlock()
	if selfFile != nil {
		return nil
	}
	f, err := openSelf()
	if err != nil {
		return fmt.Errorf("clientver: open running executable for possession proof: %w", err)
	}
	selfFile = f
	return nil
}

// AnswerChallenge returns the lowercase hex sha256 of the nonce bytes followed
// by the executable's bytes in [offset, offset+length), with NO separator.
//
// THE NONCE PARAMETER IS RAW BYTES, NOT THE ENCODED TEXT, and the type says so.
// The verifier transmits the nonce base64url-encoded and hashes its DECODED
// bytes; a caller that passed the encoded string would produce a proof that
// never matches, and no compiler on either side of the repo boundary would
// notice. Decoding is the exchange's job; hashing is this function's.
//
// DIGEST INPUT ORDERING IS NONCE FIRST, THEN THE FILE RANGE. Any other order is
// a proof the verifier rejects, again with no compile-time tell.
//
// It REFUSES, naming the offending value, when the handle is unopened, when
// length exceeds the ceiling, when either bound is negative, or when
// offset+length runs past the end of the file. It NEVER clamps and never
// returns a partial-range digest: a silently short read produces a
// wrong-but-well-formed proof that the verifier cannot distinguish from a
// spoof, so a refusal the client can see beats a digest nobody can explain.
func AnswerChallenge(nonce []byte, offset, length int64) (string, error) {
	selfMu.Lock()
	f := selfFile
	selfMu.Unlock()
	if f == nil {
		return "", fmt.Errorf("clientver: possession proof requested before the executable handle was opened")
	}
	if offset < 0 || length < 0 {
		return "", fmt.Errorf("clientver: challenge range must be non-negative, got offset %d length %d", offset, length)
	}
	if length > maxChallengeLength {
		return "", fmt.Errorf("clientver: challenge length %d exceeds this client's ceiling of %d bytes; refusing rather than answering a truncated range", length, maxChallengeLength)
	}
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("clientver: stat running executable: %w", err)
	}
	if offset+length > info.Size() {
		return "", fmt.Errorf("clientver: challenge range [%d,%d) runs past the executable's %d bytes; refusing rather than answering a short read", offset, offset+length, info.Size())
	}

	buf := make([]byte, length)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return "", fmt.Errorf("clientver: read challenge range [%d,%d) of the running executable: %w", offset, offset+length, err)
	}

	h := sha256.New()
	h.Write(nonce)
	h.Write(buf)
	return hex.EncodeToString(h.Sum(nil)), nil
}
