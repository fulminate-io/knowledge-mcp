// SPDX-License-Identifier: Apache-2.0

package hnsw

import "testing"

// TestHammingDistanceIdentity asserts a vector's distance to itself is zero.
func TestHammingDistanceIdentity(t *testing.T) {
	a := make([]byte, 32)
	for i := range a {
		a[i] = byte(i * 7)
	}
	if d := hammingDistance(a, a); d != 0 {
		t.Fatalf("hammingDistance(a,a) = %v, want 0", d)
	}
}

// TestHammingDistanceKnownBits asserts the popcount of XOR over a known
// bit-difference pair. 0x00 vs 0xFF differs in 8 bits per byte; 0x0F vs 0x00
// differs in 4 bits. This mirrors the server's distance.go:44 implementation.
func TestHammingDistanceKnownBits(t *testing.T) {
	cases := []struct {
		name string
		a    []byte
		b    []byte
		want float32
	}{
		{"all-bits-one-byte", []byte{0x00}, []byte{0xFF}, 8},
		{"nibble-one-byte", []byte{0x00}, []byte{0x0F}, 4},
		{"single-bit", []byte{0x00}, []byte{0x01}, 1},
		{"multi-byte", []byte{0x00, 0x00, 0x00}, []byte{0xFF, 0x0F, 0x01}, 8 + 4 + 1},
		// 32-byte vector exercising the 64-bit fast path (4 full uint64 chunks).
		{"32-byte-one-bit-each-word", make32(0x00), wordBits(), 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if d := hammingDistance(tc.a, tc.b); d != tc.want {
				t.Fatalf("hammingDistance = %v, want %v", d, tc.want)
			}
		})
	}
}

func make32(b byte) []byte {
	v := make([]byte, 32)
	for i := range v {
		v[i] = b
	}
	return v
}

// wordBits sets exactly one bit in each of the four 8-byte words of a 32-byte
// vector (4 bits total).
func wordBits() []byte {
	v := make([]byte, 32)
	v[0] = 0x01
	v[8] = 0x01
	v[16] = 0x01
	v[24] = 0x01
	return v
}
