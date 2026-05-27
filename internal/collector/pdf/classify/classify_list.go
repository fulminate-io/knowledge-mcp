// classify_list.go — list-item detection.
//
// isListItem matches the first-line text of a block against the
// configured list-marker regex (defaultListMarkerPattern from types.go
// when the caller passes a nil ClassifyParams.ListMarkerPattern). On
// match it surfaces the trimmed marker plus a parsed numeric index
// (decimal markers only — bullets, alphabetic, and roman markers
// return index=0).

package classify

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/collector/pdf/layout"
)

// isListItem returns whether the first line of block matches the
// list-marker regex pattern. On match, marker is the trimmed capture
// (group 1 of the regex) and index is the parsed numeric value (0 for
// non-numeric markers). Empty block, empty first line, or nil pattern
// returns false.
func isListItem(block layout.Block, pattern *regexp.Regexp) (matched bool, marker string, index int) {
	if pattern == nil {
		return false, "", 0
	}
	if len(block.Lines) == 0 || len(block.Lines[0].Runs) == 0 {
		return false, "", 0
	}
	firstLine := concatRunsText(block.Lines[0])
	if firstLine == "" {
		return false, "", 0
	}
	subs := pattern.FindStringSubmatch(firstLine)
	if len(subs) < 2 {
		return false, "", 0
	}
	marker = strings.TrimSpace(subs[1])
	index = parseListMarker(marker)
	return true, marker, index
}

// concatRunsText concatenates the Text of every run in line in
// reading order. No spaces inserted — the runs are assumed to already
// carry the inter-glyph spacing emitted by the content-stream walker.
func concatRunsText(line layout.Line) string {
	var b strings.Builder
	for _, run := range line.Runs {
		b.WriteString(run.Text)
	}
	return b.String()
}

// numericMarkerPattern matches "1.", "1)", "(1)", "(1." etc. Captures
// the leading digits in group 1.
var numericMarkerPattern = regexp.MustCompile(`^\(?(\d+)[.)]$`)

// parseListMarker extracts a numeric index from marker. Decimal-only:
// bullets, alphabetic, and roman markers return 0. Roman numerals are
// out of scope for v1 numeric extraction (the regex still recognizes
// them as list markers; just no integer mapping).
func parseListMarker(marker string) int {
	subs := numericMarkerPattern.FindStringSubmatch(marker)
	if len(subs) < 2 {
		return 0
	}
	n, err := strconv.Atoi(subs[1])
	if err != nil {
		return 0
	}
	return n
}
