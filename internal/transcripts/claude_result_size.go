// SPDX-License-Identifier: Apache-2.0

package transcripts

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// This file measures what a tool call HANDED BACK — the size of its tool_result and
// whether the call ran in the background. Those two facts are what let a later analysis
// price a tool by the context its results occupy rather than by how long it ran; a fast
// tool returning 100KB costs more than a slow one returning a line.

// claudeResultBlock is one block inside a LIST-shaped tool_result. Only text carries
// measurable bytes and only image is counted as an image; every other kind is measured at
// its raw encoded length, so a future block type is under-attributed rather than dropped.
type claudeResultBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// A spilled tool_result was written to a file and appears inline as a short notice, so its
// inline length is a few dozen bytes while the result itself was the largest the corpus
// holds. Ignoring spills would systematically understate exactly the rows this measurement
// exists to rank, so the size is RECOVERED from the notice.
//
// TWO notice formats exist and both are parsed; a single-shape probe would silently miss
// the other. Both patterns require their save clause, which is what separates a real spill
// from the near-miss below.
//
//nolint:gochecknoglobals // hoisted out of the parse hot loop per the hot-loop rule.
var (
	// spilledCharsRe matches: result (85,217 characters across 1 line) exceeds maximum
	// allowed tokens. Output has been saved to <path>.
	//
	// THE SAVED-TO CLAUSE IS LOAD-BEARING, not decoration. A tool's own refusal to read an
	// oversized file — "File content (30816 tokens) exceeds maximum allowed tokens
	// (25000). Please use offset and limit parameters" — shares the "exceeds maximum
	// allowed tokens" literal and is NOT a spill: nothing was saved and the full content
	// never existed inline. Loosening this pattern to the shorter phrase would classify
	// every one of those refusals as a spill with a fabricated byte count.
	spilledCharsRe = regexp.MustCompile(`result \(([0-9,]+) characters[^)]*\) exceeds maximum allowed tokens\..*Output has been saved to`)
	// spilledKBRe matches: Output too large (58.7KB). Full output saved to: <path>
	spilledKBRe = regexp.MustCompile(`Output too large \(([0-9.]+)KB\)\. Full output saved to:`)
)

// recoverSpilledBytes reads a spilled result's true size out of its notice. It reports
// false for any text that is not a recognized notice, INCLUDING a notice-shaped one whose
// size does not parse — in that case the caller records the inline length, an honest
// under-measurement rather than a guess.
//
// STATED LIMITATION: a THIRD notice format introduced by a future harness would not be
// recognized here and its result would be measured at its inline length, under-reporting.
// The present corpus carries only these two formats.
func recoverSpilledBytes(s string) (int64, bool) {
	if m := spilledCharsRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	if m := spilledKBRe.FindStringSubmatch(s); m != nil {
		kb, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, false
		}
		return int64(math.Round(kb * 1024)), true
	}
	return 0, false
}

// sizeResultText measures one text payload, preferring a recovered spill size over the
// inline length.
func sizeResultText(s string) (nbytes int64, spilled bool) {
	if n, ok := recoverSpilledBytes(s); ok {
		return n, true
	}
	return int64(len(s)), false
}

// measureToolResult sizes a tool_result's content. Measured shapes across 96,685 sampled
// blocks: the content is a STRING 57,650 times and a LIST 39,035 times, and the list's
// inner blocks are text (38,584), tool_reference (1,053) and image (310).
//
// It walks blocks already decoded in the same pass and sums byte lengths; it never
// re-marshals the content just to measure it, because this runs inside the parser's hot
// loop over a multi-gigabyte corpus.
func measureToolResult(content json.RawMessage) (nbytes, images int64, spilled bool) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return 0, 0, false
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return int64(len(trimmed)), 0, false
		}
		n, sp := sizeResultText(s)
		return n, 0, sp
	case '[':
		var raws []json.RawMessage
		if err := json.Unmarshal(trimmed, &raws); err != nil {
			return int64(len(trimmed)), 0, false
		}
		for _, raw := range raws {
			var b claudeResultBlock
			if err := json.Unmarshal(raw, &b); err != nil {
				nbytes += int64(len(raw))
				continue
			}
			switch b.Type {
			case "text":
				n, sp := sizeResultText(b.Text)
				nbytes += n
				spilled = spilled || sp
			case "image":
				images++
			default:
				nbytes += int64(len(raw))
			}
		}
		return nbytes, images, spilled
	default:
		return int64(len(trimmed)), 0, false
	}
}

// runInBackgroundKey is the tool_use input key naming a backgrounded run.
const runInBackgroundKey = "run_in_background"

// runInBackground reports whether a tool_use input asked for a backgrounded run. The
// substring pre-check keeps the common case (a payload without the key) to a scan rather
// than a full decode — the parser sizes a multi-gigabyte corpus and every tool_use passes
// through here.
func runInBackground(input json.RawMessage) bool {
	if !bytes.Contains(input, []byte(runInBackgroundKey)) {
		return false
	}
	var obj struct {
		RunInBackground bool `json:"run_in_background"`
	}
	if err := json.Unmarshal(input, &obj); err != nil {
		return false
	}
	return obj.RunInBackground
}
