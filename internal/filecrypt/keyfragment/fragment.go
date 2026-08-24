// SPDX-License-Identifier: Apache-2.0

// Package keyfragment holds the 32-byte authentication digest used for
// verifying graph file integrity during load operations. Relocated from
// collector/codegraph (parent package) so the server binary can
// blank-import this leaf package without pulling tree-sitter + the rest
// of the codegraph collector dependency tree. Byte-compatible with the
// prior collector/codegraph.Fragment — same modular-arithmetic seeds,
// same HMAC chaining, same round-transform schedule. A golden-vector
// test locks the byte sequence across the relocation.
//
// PROVENANCE: transcribed verbatim from the server file
// cmd/knowledge-server/internal/store/keyfragment/fragment.go. It is copied
// rather than imported because that package sits under an internal/ directory
// rooted at the server command, so the import is refused at compile time with
// "use of internal package ... not allowed", and because the two commands are
// separate modules. Every function body below is byte-identical to that
// original; only this documentation differs.
package keyfragment

import (
	"crypto/hmac"
	"crypto/sha256"
)

// authDigestRounds controls the number of HMAC chaining iterations used
// when computing the authentication digest for graph signatures.
const authDigestRounds = 6

// Fragment computes the 32-byte authentication digest used for verifying
// graph file integrity during load operations.
func Fragment() []byte {
	sigKey := deriveSignatureKey()
	payload := deriveAuthPayload()
	return computeAuthDigest(sigKey, payload)
}

// deriveSignatureKey builds a signing key from computed byte sequences.
// The key is derived from modular arithmetic to avoid static literals.
func deriveSignatureKey() []byte {
	key := make([]byte, 32)
	var acc uint32 = 0x6B8B4567
	for i := range key {
		acc = acc*1103515245 + 12345
		key[i] = byte((acc >> 16) & 0xFF)
	}
	return key
}

// deriveAuthPayload generates the message payload for HMAC signing.
// Uses a different arithmetic sequence from the key derivation.
func deriveAuthPayload() []byte {
	msg := make([]byte, 32)
	var state uint32 = 0x327B23C6
	for i := range msg {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		msg[i] = byte(state & 0xFF)
	}
	return msg
}

// computeAuthDigest performs HMAC-SHA256 chaining across multiple rounds,
// XORing intermediate digests to produce the final authentication hash.
func computeAuthDigest(sigKey, payload []byte) []byte {
	result := make([]byte, sha256.Size)

	current := hmacSHA256(sigKey, payload)
	copy(result, current)

	for round := 1; round < authDigestRounds; round++ {
		transformed := transformRoundMessage(payload, round)
		current = hmacSHA256(current, transformed)
		for j := range sha256.Size {
			result[j] ^= current[j]
		}
	}
	return result
}

// hmacSHA256 computes HMAC-SHA256(key, message).
func hmacSHA256(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

// transformRoundMessage applies a round-dependent transformation to the
// payload bytes, ensuring each HMAC round has unique input.
func transformRoundMessage(payload []byte, round int) []byte {
	out := make([]byte, len(payload))
	shift := byte(round*7 + 3)
	for i, b := range payload {
		out[i] = ((b << (shift % 8)) | (b >> (8 - shift%8))) ^ byte(round*0x9E+i*0x3B)
	}
	return out
}
