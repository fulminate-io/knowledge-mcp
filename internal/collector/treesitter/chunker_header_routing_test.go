// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"context"
	"fmt"
	"sort"
	"testing"
)

// TestChunkFile_DotHRoutesByParse pins both directions of the .h fallback: a
// C++ header named .h is adopted by the cpp grammar once the C parse errors,
// and a plain C header is left on the C grammar untouched. The control matters
// as much as the subject — asserting only the C++ side would pass on a change
// that routed every .h to cpp.
func TestChunkFile_DotHRoutesByParse(t *testing.T) {
	const cppHeader = `#pragma once

namespace app {

template <typename T>
class Box {
 public:
  T get() const { return value_; }

 private:
  T value_;
};

}  // namespace app

int helper(int x) { return x + 1; }
`

	const cHeader = `#ifndef C_H
#define C_H

struct point {
  int x;
  int y;
};

int add(int a, int b);

#endif
`

	cases := []struct {
		desc     string
		path     string
		src      string
		wantLang Language
		wantSet  []string
	}{
		{
			desc:     "cpp_header_adopts_cpp_grammar",
			path:     "inc/cpp.h",
			src:      cppHeader,
			wantLang: LangCPP,
			wantSet: []string{
				"(template_declaration)",
				"app(namespace_definition)",
				"Box(class_specifier)",
				"get(function_definition)",
				"helper(function_definition)",
			},
		},
		{
			desc:     "c_header_stays_c_control",
			path:     "inc/c.h",
			src:      cHeader,
			wantLang: LangC,
			wantSet: []string{
				"(declaration)",
				"(preproc_ifdef)",
				"point(struct_specifier)",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			chunker := NewChunker()
			defer chunker.Close()

			res, err := chunker.ChunkFile(context.Background(), tc.path, []byte(tc.src))
			if err != nil {
				t.Fatalf("ChunkFile: %v", err)
			}
			if res.Language != tc.wantLang {
				t.Errorf("Language = %q; want %q", res.Language, tc.wantLang)
			}

			got := make([]string, 0, len(res.Chunks))
			for _, c := range res.Chunks {
				got = append(got, fmt.Sprintf("%s(%s)", c.Name, c.ChunkType))
			}
			sort.Strings(got)
			want := append([]string(nil), tc.wantSet...)
			sort.Strings(want)

			if len(got) != len(want) {
				t.Fatalf("chunk set has %d entries; want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("chunk set mismatch at %d: got %q want %q\n got: %v\nwant: %v", i, got[i], want[i], got, want)
				}
			}
		})
	}
}
