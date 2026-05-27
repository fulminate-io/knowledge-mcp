package font

// glyphlist.txt is the Adobe Glyph List (AGL) version 2.0, sourced
// from github.com/adobe-type-tools/agl-aglfn @ commit
// 4036a9ca80a62f64f9de4f7321a9a045ad0ecfd6 (committed 2019-10-31; AGL
// has been frozen since v2.0 in 2002).
//
// Verbatim license attribution (BSD-style 3-clause Adobe license, the
// upstream file's header lines 1-39):
//
//   Copyright 2002-2019 Adobe (http://www.adobe.com/).
//
//   Redistribution and use in source and binary forms, with or
//   without modification, are permitted provided that the
//   following conditions are met:
//
//   Redistributions of source code must retain the above
//   copyright notice, this list of conditions and the following
//   disclaimer.
//
//   Redistributions in binary form must reproduce the above
//   copyright notice, this list of conditions and the following
//   disclaimer in the documentation and/or other materials
//   provided with the distribution.
//
//   Neither the name of Adobe nor the names of its contributors
//   may be used to endorse or promote products derived from this
//   software without specific prior written permission.
//
//   THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND
//   CONTRIBUTORS "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES,
//   INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF
//   MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
//   DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR
//   CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
//   SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT
//   NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES;
//   LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
//   HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
//   CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR
//   OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
//   SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
//
// File format: each non-comment line is `glyphname;HHHH` or
// `glyphname;HHHH HHHH` for ligatures, where HHHH is a 4-digit
// uppercase hex Unicode scalar value. The map returns []rune so
// multi-codepoint ligatures decompose correctly when emitted as UTF-8.

import (
	_ "embed"
	"strconv"
	"strings"
	"sync"
)

//go:embed glyphlist.txt
var glyphlistData string

var (
	aglOnce sync.Once
	aglMap  map[string][]rune
)

// lookupGlyph maps an Adobe glyph name to its Unicode scalar value(s).
// Returns ok=false for unknown names and for the canonical /.notdef
// (which has no Unicode mapping per the AGL convention). Ligature
// glyph names like "fi" return multi-rune slices ([0x66, 0x69]).
//
// Package-private: the resolver is the only intended caller.
func lookupGlyph(name string) ([]rune, bool) {
	aglOnce.Do(parseAGL)
	rs, ok := aglMap[name]
	return rs, ok
}

// parseAGL decodes the embedded glyphlist.txt into aglMap. Called once
// via sync.Once on the first lookupGlyph invocation. Lines starting
// with '#' or empty lines are skipped; lines with malformed hex
// codepoints are silently dropped (the upstream file is curated, so
// any malformed line is a bug we surface via the build break elsewhere).
func parseAGL() {
	aglMap = make(map[string][]rune, 4500)
	for line := range strings.SplitSeq(glyphlistData, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		before, after, ok := strings.Cut(line, ";")
		if !ok {
			continue
		}
		name := before
		hexes := strings.Fields(after)
		if len(hexes) == 0 {
			continue
		}
		runes := make([]rune, 0, len(hexes))
		for _, h := range hexes {
			n, err := strconv.ParseUint(h, 16, 32)
			if err != nil {
				continue
			}
			runes = append(runes, rune(n))
		}
		if len(runes) > 0 {
			aglMap[name] = runes
		}
	}
}
