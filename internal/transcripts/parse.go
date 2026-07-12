// SPDX-License-Identifier: Apache-2.0

package transcripts

import "fmt"

// Parse column-extracts a single enumerated transcript file into Rows, opening
// e.Path and dispatching on e.Source to the format-specific parser. It is the
// per-file unit KN-2 composes with Enumerate: Enumerate() → []Entry, then
// Parse(entry) → []Row per file. Choosing WHICH files to (re)parse and any
// parallelism across files is KN-2's consumption concern — Parse deliberately
// has no corpus loop or fan-out.
func Parse(e Entry) ([]Row, error) {
	switch e.Source {
	case SourceClaude:
		return parseClaudeFile(e.Path)
	case SourceCodex:
		return parseCodexFile(e.Path)
	default:
		return nil, fmt.Errorf("transcripts: unknown source %q for %s", e.Source, e.Path)
	}
}
