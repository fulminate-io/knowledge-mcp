// SPDX-License-Identifier: Apache-2.0

// Package docgen is the cmd/knowledge client-side documentation generator. It
// walks the live tool catalog (tools.AllToolSchemas) and the client CLI
// FlagSets (the register* seams in package bootstrap) and renders deterministic
// markdown tables into named managed blocks inside the committed docs/guides/
// tree. `go generate ./...` runs it; a CI drift gate re-runs it and diffs.
//
// Generated content lives ONLY between named HTML-comment markers:
//
//	<!-- BEGIN GENERATED: <id> -->
//	...generated table...
//	<!-- END GENERATED: <id> -->
//
// Everything outside the markers — the hand-written prose sections — is
// preserved byte-for-byte. The splicer errors loudly when a marker is missing
// (never silently appends), so a future tool/flag addition whose scaffold block
// is absent produces an actionable CI failure rather than a quietly-appended
// block.
package docgen

import (
	"fmt"
	"strings"
)

// Marker format strings for a named generated block. blockName is interpolated
// so a single file can carry MULTIPLE independent generated regions (binaries.md
// holds one flag table per subcommand). Deliberately distinct from
// bootstrap's single-global `knowledge-managed` marker pair: these are per-block
// named, and the splicer errors on a missing marker instead of appending.
const (
	markerBeginFmt = "<!-- BEGIN GENERATED: %s -->"
	markerEndFmt   = "<!-- END GENERATED: %s -->"
)

// spliceManagedBlock returns content with the named generated block's body
// replaced by body, preserving everything outside the marker pair byte-for-byte.
//
// The region between `<!-- BEGIN GENERATED: <blockName> -->` and
// `<!-- END GENERATED: <blockName> -->` (markers themselves preserved) is
// replaced with: a newline, body, a trailing newline. Re-running with the same
// body is idempotent.
//
// Returns a non-nil error — and NEVER appends — when either marker for blockName
// is absent, or when the END marker precedes the BEGIN marker. Loud failure is
// the point: a missing marker means the scaffold is out of sync with the tool/
// flag catalog, which the CI drift gate must surface.
func spliceManagedBlock(content, blockName, body string) (string, error) {
	begin := fmt.Sprintf(markerBeginFmt, blockName)
	end := fmt.Sprintf(markerEndFmt, blockName)

	beginIdx := strings.Index(content, begin)
	if beginIdx < 0 {
		return "", fmt.Errorf("docgen: BEGIN marker %q not found", begin)
	}
	endIdx := strings.Index(content, end)
	if endIdx < 0 {
		return "", fmt.Errorf("docgen: END marker %q not found", end)
	}
	if endIdx < beginIdx {
		return "", fmt.Errorf("docgen: END marker %q precedes BEGIN marker %q for block %q", end, begin, blockName)
	}

	before := content[:beginIdx+len(begin)]
	after := content[endIdx:]

	var b strings.Builder
	b.WriteString(before)
	b.WriteString("\n")
	b.WriteString(body)
	b.WriteString("\n")
	b.WriteString(after)
	return b.String(), nil
}
