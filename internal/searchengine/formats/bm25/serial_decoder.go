// SPDX-License-Identifier: Apache-2.0

package bm25

import (
	"encoding/binary"
	"fmt"
	"math"
)

// decoder is a bounds-checked little-endian cursor over an Encode() blob. Every
// read advances pos and returns an error (never panics) on truncation, so a
// malformed/short blob is rejected rather than crashing the decode goroutine.
type decoder struct {
	data []byte
	pos  int
}

func (d *decoder) byte() (byte, error) {
	if d.pos+1 > len(d.data) {
		return 0, fmt.Errorf("bm25 decode: truncated byte at %d", d.pos)
	}
	b := d.data[d.pos]
	d.pos++
	return b, nil
}

func (d *decoder) u16() (uint16, error) {
	if d.pos+2 > len(d.data) {
		return 0, fmt.Errorf("bm25 decode: truncated uint16 at %d", d.pos)
	}
	v := binary.LittleEndian.Uint16(d.data[d.pos:])
	d.pos += 2
	return v, nil
}

func (d *decoder) u32() (uint32, error) {
	if d.pos+4 > len(d.data) {
		return 0, fmt.Errorf("bm25 decode: truncated uint32 at %d", d.pos)
	}
	v := binary.LittleEndian.Uint32(d.data[d.pos:])
	d.pos += 4
	return v, nil
}

func (d *decoder) u64() (uint64, error) {
	if d.pos+8 > len(d.data) {
		return 0, fmt.Errorf("bm25 decode: truncated uint64 at %d", d.pos)
	}
	v := binary.LittleEndian.Uint64(d.data[d.pos:])
	d.pos += 8
	return v, nil
}

// lenPrefixedString reads a uint16 length prefix then that many bytes as a string.
func (d *decoder) lenPrefixedString() (string, error) {
	n, err := d.u16()
	if err != nil {
		return "", err
	}
	if d.pos+int(n) > len(d.data) {
		return "", fmt.Errorf("bm25 decode: truncated string (need %d at %d)", n, d.pos)
	}
	s := string(d.data[d.pos : d.pos+int(n)])
	d.pos += int(n)
	return s, nil
}

// mathFloatBits / mathFloatFromBits bridge float64 ↔ its IEEE-754 bit pattern for
// lossless serialization of the field boost/B parameters.
func mathFloatBits(f float64) uint64     { return math.Float64bits(f) }
func mathFloatFromBits(b uint64) float64 { return math.Float64frombits(b) }
