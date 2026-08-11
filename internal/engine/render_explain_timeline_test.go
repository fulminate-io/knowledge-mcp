// SPDX-License-Identifier: Apache-2.0

package engine

import (
	"math"
	"testing"
)

// TestNewTimelineTopK_RowCapBounds pins the constructor's own bounds on rowCap.
//
// NewTimelineTopK is EXPORTED and sizes its retention buffer from the rowCap it
// is handed, so the bound has to live here rather than only at the one call site
// that happens to clamp today: an enormous rowCap made the capacity hint exceed
// what a slice allocation accepts and panicked the constructor outright.
//
// The three cases are one guard plus its two known-positive controls — an
// in-range value must survive unchanged and a non-positive one must still fall
// back to the default, so a clamp that swallowed every caller's limit would fail
// here rather than pass as "bounded".
func TestNewTimelineTopK_RowCapBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		given int
		want  int
	}{
		{"MaxInt is clamped to the ceiling", math.MaxInt, TimelineRowCapMax},
		{"above the ceiling is clamped", TimelineRowCapMax + 1, TimelineRowCapMax},
		{"an in-range limit passes through", 42, 42},
		{"non-positive falls back to the default", 0, TimelineRowCapDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := NewTimelineTopK("CreatedAt", tc.given)
			if k.rowCap != tc.want {
				t.Fatalf("rowCap = %d, want %d", k.rowCap, tc.want)
			}
			if c := cap(k.entries); c > TimelineRowCapMax+BrowsePageSize {
				t.Fatalf("retention buffer capacity %d exceeds the bound %d",
					c, TimelineRowCapMax+BrowsePageSize)
			}
		})
	}
}
