// SPDX-License-Identifier: Apache-2.0

package embed

import (
	"encoding/binary"
	"math"
)

// encodeFloat32LE turns one row of decoded float values into the bytes every
// hop downstream carries: FOUR BYTES PER VALUE, LITTLE-ENDIAN.
//
// THE BYTE ORDER IS A CONTRACT RATHER THAN A PREFERENCE, and that is why this
// is one function instead of a loop copied into each arm that decodes floats.
// The v3 segment's float view reads its mapped bytes as little-endian float32
// (searchengine/formats/hnsw), so an encoder that disagreed with it would not
// fail — it would produce finite, plausible, WRONG distances, which is the worst
// failure mode available here. A second copy of the loop is a second chance to
// disagree with that reader, and the disagreement is silent in both directions.
//
// THE EMITTED WIDTH FOLLOWS FROM THE RESPONSE, and is not re-derived from the
// configured dimension: each returned value becomes four bytes, so a row of N
// values weighs 4N. A provider that honored the requested width produces the
// expected weight, and one that did not produces a different weight rather than
// a silently reshaped vector.
//
// IT IS GENERIC OVER THE TWO FLOAT WIDTHS THE ARMS DECODE INTO because they
// genuinely differ and the difference is not this function's business: the
// Voyage arm holds its response undecoded and unmarshals it as float64 so a
// representation mismatch stays detectable, while the two float-only arms decode
// straight into float32. Both reach the same encoder rather than each keeping
// its own.
func encodeFloat32LE[T ~float32 | ~float64](values []T) []byte {
	out := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(float32(v)))
	}
	return out
}
