// SPDX-License-Identifier: Apache-2.0

// code_search_degrade.go — the per-call record of search legs that FAILED, and
// the one line that puts them in front of the caller. A code search whose engine
// leg errors used to render an ordinary empty or partial result under an
// "index: up to date" footer; a wrong answer that looks healthy is worse than an
// error, because nothing prompts the reader to doubt it.

package tools

import (
	"slices"
	"strings"
	"sync"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// searchDegradedMarker prefixes the rendered degrade line. Kept whole on ONE
// source line: the render, the tests and future readers all anchor on it.
const searchDegradedMarker = "search degraded: "

// searchDegrade collects the reasons a single code search served less than it was
// asked for. One value is allocated per search call and shared by the per-query
// and per-repo goroutine fan-outs, hence the mutex.
//
// BOTH METHODS ARE NIL-RECEIVER-SAFE, and that is a contract rather than
// decoration: the sub-composers are driven directly by unit tests through a
// codeSearchDeps literal that sets no degrade field, so the pointer is legitimately
// nil on those paths. A method that dereferenced would panic them.
type searchDegrade struct {
	mu      sync.Mutex
	reasons []string
}

// record notes one failed leg. Identical reasons are de-duplicated: the per-query
// and per-repo fan-outs can hit the same failure many times over, and one line per
// query would be noise rather than signal.
func (d *searchDegrade) record(reason string) {
	if d == nil || reason == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if slices.Contains(d.reasons, reason) {
		return
	}
	d.reasons = append(d.reasons, reason)
}

// banner returns the single rendered line, or "" when nothing was recorded (and
// on a nil receiver).
func (d *searchDegrade) banner() string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.reasons) == 0 {
		return ""
	}
	return searchDegradedMarker + strings.Join(d.reasons, "; ")
}

// appendDegradeContent APPENDS the degrade line to a rendered result as a second
// text item, leaving the result unchanged when nothing degraded.
//
// APPENDING IS REQUIRED, not stylistic: this is the json path, and content[0]
// must stay the parseable envelope every json consumer reads. Prepending the
// marker, or folding it into the envelope, breaks all of them.
func appendDegradeContent(res kgtools.ToolResult, d *searchDegrade) kgtools.ToolResult {
	banner := d.banner()
	if banner == "" {
		return res
	}
	res.Content = append(res.Content, kgtools.ContentBlock{Type: "text", Text: banner})
	return res
}
