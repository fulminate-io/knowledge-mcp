// SPDX-License-Identifier: Apache-2.0

// unknown_operation.go — the single source of the canonical unknown-operation
// diagnostic shared by every operation-dispatched tool in this package.
//
// Post-cutover there is no server-side fallback: a tool call the client does
// not claim lands on the engine's tool-level deny, which says the TOOL has no
// client intercept. For a tool that does have one and merely received an
// operation outside its vocabulary, that message is false on its face. Every
// operation-dispatched tool therefore terminates its own switch with this
// diagnostic, which names the tool, quotes the offending operation, and lists
// what it would have accepted.

package tools

import (
	"fmt"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// unknownOperationMessage renders the canonical text. It is split out from
// unknownOperationResult so a caller that must append a tool-specific note
// (sync's "promote was removed" history) still composes the ONE canonical
// prefix rather than spelling out a second message shape.
func unknownOperationMessage(tool, op string, valid []string) string {
	return fmt.Sprintf("%s: unknown operation %q — valid operations: %s",
		tool, op, strings.Join(valid, ", "))
}

// unknownOperationResult is the terminal result an operation-dispatched tool
// returns when the caller named an operation the tool does not implement. The
// %q verb quotes the operation so an empty or whitespace-only one is visible
// in the output rather than vanishing into the sentence.
func unknownOperationResult(tool, op string, valid []string) kgtools.ToolResult {
	return errorResult(unknownOperationMessage(tool, op, valid))
}
