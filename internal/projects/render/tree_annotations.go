// SPDX-License-Identifier: Apache-2.0

package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

// tree_annotations.go composes the per-node annotation line the tree renderer
// emits beside a section that carries reviewer annotations.

// annotationKindOrder is the FIXED order the three annotation kinds render in.
//
// IT IS THE VOCABULARY'S OWN ORDER, read from kgtypes rather than re-listed
// here. The same three words are what the pre-write guard validates against and
// what a refusal message lists, so a second hand-written copy would let the
// renderer report a kind the guard rejects, or fall silent on one it admits.
//
// A FIXED ORDER RATHER THAN THE READ'S ORDER, because the same annotation set
// must render the same way on every run: the census below is a map, and a Go map
// range is randomized, so an order taken from the data would make one unchanged
// plan render differently from one read to the next and turn every byte
// comparison into a flake.
var annotationKindOrder = kgtypes.AnnotationKinds

// AnnotationLine composes the tree's annotation line from the kinds of the
// annotations attached to one node: `annotations: 4 (correct 1, finding 2,
// needed change 1)`.
//
// AN EMPTY INPUT COMPOSES AN EMPTY LINE, which is what lets the renderer omit
// the line entirely rather than render a zero — the distinction the
// one-version-back rule turns on.
//
// AN UNRECOGNIZED KIND IS SHOWN, not dropped. The read found an annotation; a
// kind this renderer does not know is a reason to name it, not a reason to
// under-report the review state. Unrecognized kinds sort after the three known
// ones, alphabetically among themselves, so the order stays fixed.
func AnnotationLine(kinds []string) string {
	if len(kinds) == 0 {
		return ""
	}
	census := map[string]int{}
	for _, k := range kinds {
		census[k]++
	}

	var extra []string
	for k := range census {
		if !knownAnnotationKind(k) {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)

	parts := make([]string, 0, len(census))
	for _, k := range append(append([]string{}, annotationKindOrder...), extra...) {
		if n := census[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
		}
	}
	return fmt.Sprintf("annotations: %d (%s)", len(kinds), strings.Join(parts, ", "))
}

// knownAnnotationKind reports whether kind is one of the three the annotation
// vocabulary defines.
func knownAnnotationKind(kind string) bool {
	return kgtypes.IsAnnotationKind(kind)
}
