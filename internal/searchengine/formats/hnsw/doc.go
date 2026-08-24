// SPDX-License-Identifier: Apache-2.0

// Package hnsw is the binary-HNSW SegmentFormat for the segmented search engine:
// a sealed, immutable approximate-nearest-neighbor graph over 256-bit binary
// vectors, searched by Hamming distance.
//
// DECLARED CONSTRAINT: LITTLE-ENDIAN ONLY. The serialVersion-3 layout is read in
// place, which means the typed views a reader takes over the blob are HOST-ENDIAN
// casts rather than decoded integers. Every target this ships to is
// little-endian, so the format declares that rather than paying a byte-swapping
// read path on every access for a platform nobody runs. A big-endian host is
// UNSUPPORTED — explicitly, so it fails as an unmet requirement rather than
// being silently mis-served plausible-looking neighbors. This is bm25's posture
// for the same reason.
//
// EVERY OFFSET IN THE BLOB IS ABSOLUTE FROM BLOB START. That holds for the header
// fields and equally for the layerOffsets entries, which are absolute positions
// into the neighbor arena rather than arena-relative displacements. Stating it
// once here is what stops a later reader re-deriving the layer offsets against
// the wrong base — a mistake that would produce wrong neighbors rather than a
// crash, which is the failure mode this format is most exposed to.
package hnsw
