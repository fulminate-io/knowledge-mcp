// SPDX-License-Identifier: Apache-2.0

// Package keyfragment composes the client's master encryption key from
// per-host machine identity + a set of per-build key fragments. The parent
// filecrypt package receives only the finished 32-byte master key —
// composition lives here so callers don't import encryption packages or know
// how keys are derived.
//
// Defense-in-depth: each fragment is implemented in its own sub-package
// using a distinct algorithmic approach so reconstructing the master key
// from binary analysis alone is harder than reading one constant.
//
// PROVENANCE: transcribed verbatim from the server package
// cmd/knowledge-server/internal/store/keyfragment. It is copied rather than
// imported because that package sits under an internal/ directory rooted at
// the server command, so the import is refused at compile time with "use of
// internal package ... not allowed", and because the two commands are separate
// modules. Resolving it with a hand-written package shared by both binaries is
// denied by this repo's architecture invariant, which admits only generated
// protobuf as a cross-module contract. Every function body below is
// byte-identical to that original; only this documentation differs.
package keyfragment

import (
	"crypto/sha256"
	"io"

	"golang.org/x/crypto/hkdf"
)

// KeyFragment is a function that returns exactly 32 bytes of key material.
// Each fragment is implemented in a different sub-package and uses a distinct
// algorithmic approach to derive its bytes.
type KeyFragment func() []byte

// DeriveMasterKey combines fragment outputs (XOR) and mixes in the machine
// identity via HKDF-SHA256 to derive the 32-byte master encryption key.
// Returns nil if no fragments are supplied or the machine ID is empty.
func DeriveMasterKey(frags []KeyFragment, machineID string) []byte {
	if len(frags) == 0 || machineID == "" {
		return nil
	}

	combined := make([]byte, 32)
	for _, frag := range frags {
		combined = xorBytes(combined, frag())
	}

	r := hkdf.New(sha256.New, combined, []byte(machineID), []byte("knowledge-encryption-v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		panic("hkdf master key derivation failed: " + err.Error())
	}
	return key
}

// xorBytes XORs two equal-length byte slices and returns the result.
// If slices differ in length, uses the shorter length.
func xorBytes(a, b []byte) []byte {
	n := min(len(a), len(b))
	out := make([]byte, n)
	for i := range n {
		out[i] = a[i] ^ b[i]
	}
	return out
}
