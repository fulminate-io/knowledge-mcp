// SPDX-License-Identifier: Apache-2.0

package hnsw

import (
	"encoding/binary"
	"math/bits"
)

// hammingDistance computes the Hamming distance between two equal-length byte
// slices using 64-bit popcount chunks, falling back to per-byte popcount for the
// trailing bytes. This is the ONLY distance the binary HNSW path needs — the
// server's cosineDistance/Normalize (which pull github.com/tphakala/simd, a
// float-only dependency) are deliberately NOT copied here.
//
// Originally copied verbatim from the server's binary HNSW distance function;
// that server-side index has since been retired (search is client-served), so
// this client-side format is now the sole implementation.
func hammingDistance(a, b []byte) float32 {
	dist := 0
	i := 0
	for ; i+8 <= len(a); i += 8 {
		x := binary.LittleEndian.Uint64(a[i:]) ^ binary.LittleEndian.Uint64(b[i:])
		dist += bits.OnesCount64(x)
	}
	for ; i < len(a); i++ {
		dist += bits.OnesCount8(a[i] ^ b[i])
	}
	return float32(dist)
}
