package fixturelib

import (
	"bytes"
	"fmt"
)

// T4ParagraphSimpleBody emits 3 body lines at Helvetica /F1 12pt
// rendered at X=72 with Y=720 / 700 / 680 (PDF bottom-up). Used by
// the T4 layout-clusterer integration fixture
// t4_paragraph_simple.pdf.
func T4ParagraphSimpleBody() string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "BT /F1 12 Tf")
	fmt.Fprintln(&b, "72 720 Td (The quick brown fox) Tj")
	fmt.Fprintln(&b, "0 -20 Td (jumps over the lazy) Tj")
	fmt.Fprintln(&b, "0 -20 Td (dog and runs away.) Tj")
	fmt.Fprintln(&b, "ET")
	return b.String()
}

// T4HyphenatedParagraphBody emits 2 body lines that exercise the
// dehyphenation rule: line 1 ends with 'inter-' and line 2 begins
// with 'national'. Used by t4_hyphenated_paragraph.pdf.
func T4HyphenatedParagraphBody() string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "BT /F1 12 Tf")
	fmt.Fprintln(&b, "72 720 Td (The quick brown fox jumps over an inter-) Tj")
	fmt.Fprintln(&b, "0 -20 Td (national dog and runs away.) Tj")
	fmt.Fprintln(&b, "ET")
	return b.String()
}

// T4Rotated90Body emits a single-line content stream for a /Rotate
// 90 page; the text appears upright after the viewer applies the
// 90° rotation. Y=720 in unrotated coordinates.
func T4Rotated90Body() string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "BT /F1 12 Tf")
	fmt.Fprintln(&b, "72 720 Td (Rotated text test.) Tj")
	fmt.Fprintln(&b, "ET")
	return b.String()
}

// T4MixedFontParagraphBody emits 3 body lines @ 12pt followed by 1
// caption line @ 8pt. The caption sits 40pt below the body so the
// inter-line gap exceeds medianGap × ParagraphGapRatio. Drives the
// median-normalization stress test (caption line's small font would
// skew per-line normalization but is corrected by per-page median).
func T4MixedFontParagraphBody() string {
	var b bytes.Buffer
	fmt.Fprintln(&b, "BT /F1 12 Tf")
	fmt.Fprintln(&b, "72 720 Td (The quick brown fox) Tj")
	fmt.Fprintln(&b, "0 -20 Td (jumps over the lazy) Tj")
	fmt.Fprintln(&b, "0 -20 Td (dog and runs away.) Tj")
	fmt.Fprintln(&b, "ET")
	// Caption: switch font size and reposition. Y=640 absolute (40pt
	// gap from the last body line at Y=680).
	fmt.Fprintln(&b, "BT /F1 8 Tf")
	fmt.Fprintln(&b, "72 640 Td (Figure 1: caption text.) Tj")
	fmt.Fprintln(&b, "ET")
	return b.String()
}
