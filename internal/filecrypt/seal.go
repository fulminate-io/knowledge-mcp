// SPDX-License-Identifier: Apache-2.0

package filecrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// magicKCE1 is the 4-byte header identifying a file sealed by this package.
// It is deliberately distinct from the magic the server writes on its own
// files, so neither binary can mistake the other's output for its own.
var magicKCE1 = []byte{'K', 'C', 'E', '1'}

// clientKeyVersion identifies the key derivation version. Increment when key
// derivation changes, so a file written under an older scheme is refused by
// name instead of failing authentication for an unexplained reason.
const clientKeyVersion byte = 0x01

// clientFileInfo is the HKDF info string for per-file key derivation. It is
// distinct from the string the server uses for its own files, which is what
// keeps the two binaries from deriving the same file key at the same path.
const clientFileInfo = "knowledge-client-file-v1"

// nonceLen is the AES-GCM nonce length the envelope layout reserves.
const nonceLen = 12

// headerLen is the fixed prefix of a sealed blob: magic(4) + keyVersion(1) +
// nonce(12). The nonce therefore occupies bytes [5:headerLen], and the
// ciphertext-and-tag runs from headerLen to the end.
const headerLen = 4 + 1 + nonceLen

// gcmTagLen is the AES-GCM authentication tag appended to every ciphertext.
const gcmTagLen = 16

// ErrLegacyPlaintext is what Open returns for a blob carrying no KCE1 magic —
// a record written before this package sealed anything. Callers distinguish it
// from a decryption failure because the two call for different responses: a
// legacy record is dropped and rebuilt, while a decryption failure means a
// sealed file will not open under the key currently in force.
var ErrLegacyPlaintext = errors.New("legacy plaintext record")

// ErrNoMasterKey is what per-file key derivation returns when the master key is
// not exactly 32 bytes. It reports the length it was given and never the bytes.
var ErrNoMasterKey = errors.New("master key is not 32 bytes")

// deriveFileKey derives a per-file AES-256 key from the master key and the file
// path, using HKDF-SHA256 with the path as salt and this binary's own info
// string. Binding the path through the salt is what makes a blob sealed for one
// path fail to open at another.
//
// THE LENGTH CHECK RUNS BEFORE THE DERIVE, and that ordering is the point of
// the check rather than an implementation detail. HKDF accepts an empty secret
// without erroring: it returns 32 deterministic bytes computed from nothing but
// the path and the info string, which anyone holding this binary can recompute.
// A check placed after the derive would be inspecting a key that had already
// been built from nothing.
func deriveFileKey(master []byte, filePath string) ([]byte, error) {
	if len(master) != 32 {
		return nil, fmt.Errorf("%w (got %d bytes)", ErrNoMasterKey, len(master))
	}
	r := hkdf.New(sha256.New, master, []byte(filePath), []byte(clientFileInfo))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("hkdf file key derivation failed: %w", err)
	}
	return key, nil
}

// newFileGCM derives the per-file key for filePath and wraps it in AES-256-GCM.
func newFileGCM(filePath string) (cipher.AEAD, error) {
	fileKey, err := deriveFileKey(MasterKey(), filePath)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(fileKey)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	if gcm.NonceSize() != nonceLen {
		return nil, fmt.Errorf("GCM nonce size %d does not match the %d-byte envelope layout", gcm.NonceSize(), nonceLen)
	}
	return gcm, nil
}

// Seal encrypts plaintext for storage at filePath and returns the whole
// envelope: magic + key version + a fresh nonce + AES-256-GCM ciphertext-and-tag.
//
// EVERY CALL ENCRYPTS. There is no branch that returns the input unchanged, no
// "not configured" shortcut and no trivial-payload shortcut — an empty payload
// still comes back as a full envelope. A failure returns an error and no bytes,
// so a caller cannot write plaintext by ignoring one.
//
// The nonce is freshly generated per call from crypto/rand. Reusing a nonce
// under one key is a total loss of GCM's guarantees, not a style question.
func Seal(filePath string, plaintext []byte) ([]byte, error) {
	gcm, err := newFileGCM(filePath)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	out := make([]byte, 0, headerLen+len(ciphertext))
	out = append(out, magicKCE1...)
	out = append(out, clientKeyVersion)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out, nil
}

// Open decrypts a blob that Seal produced for the same filePath.
//
// Three failure CLASSES, each with its own diagnosis because each calls for a
// different response:
//
//  1. the blob cannot be one of ours — the file predates encryption. The error
//     wraps ErrLegacyPlaintext so a caller can match on it and drop the record
//     rather than treating it as damage. Two distinguishable causes report
//     separately: a blob too short to hold the header, and a blob long enough
//     but carrying someone else's magic. Collapsing them would tell a truncated
//     KCE1 file it has "no KCE1 envelope header" when it plainly has one.
//  2. the key version byte is one this build does not recognize — a distinct
//     error naming the byte, so the file is refused by version rather than
//     failing authentication for no stated reason.
//  3. authentication fails — a distinct error. The likeliest cause is that the
//     machine identity the key is bound to has changed, so the message says so.
func Open(filePath string, blob []byte) ([]byte, error) {
	if len(blob) < headerLen {
		return nil, fmt.Errorf("filecrypt: %w at %s: blob is %d bytes, shorter than the %d-byte envelope header", ErrLegacyPlaintext, filePath, len(blob), headerLen)
	}
	if !bytes.Equal(blob[:len(magicKCE1)], magicKCE1) {
		return nil, fmt.Errorf("filecrypt: %w at %s: no KCE1 envelope header", ErrLegacyPlaintext, filePath)
	}
	if v := blob[len(magicKCE1)]; v != clientKeyVersion {
		return nil, fmt.Errorf("filecrypt: unsupported key version 0x%02X at %s", v, filePath)
	}
	gcm, err := newFileGCM(filePath)
	if err != nil {
		return nil, err
	}
	nonce := blob[len(magicKCE1)+1 : headerLen]
	plaintext, err := gcm.Open(nil, nonce, blob[headerLen:], nil)
	if err != nil {
		return nil, fmt.Errorf("filecrypt: decrypt failed at %s (this machine's identity may have changed since the file was written): %w", filePath, err)
	}
	return plaintext, nil
}
