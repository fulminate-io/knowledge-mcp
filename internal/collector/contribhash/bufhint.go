// SPDX-License-Identifier: Apache-2.0

package contribhash

import knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"

// maxBufHint bounds every capacity hint this package computes, at 64 MiB.
//
// IT IS A HINT CEILING, NOT A LIMIT ON WHAT IS HASHED. A node or edge whose
// encoded form is larger is hashed in full — append grows the buffer past the
// hint exactly as it would have without one. All the ceiling does is keep the
// value handed to make bounded by a constant, so no sum of caller-supplied
// field lengths can reach the allocator as an unbounded arithmetic result.
const maxBufHint = 64 << 20

// bufHint sums the parts of a capacity hint and returns the total, saturating
// at maxBufHint.
//
// THE BOUND IS CHECKED BEFORE EACH ADD, NOT AFTER: `total > maxBufHint-p` is
// the form that cannot itself wrap, which is the whole reason these sums are
// routed through one function rather than written inline at the make(). A
// negative part — which len never produces, but which a future caller could
// pass — saturates rather than silently shrinking the hint.
func bufHint(parts ...int) int {
	total := 0
	for _, p := range parts {
		if p < 0 || total > maxBufHint-p {
			return maxBufHint
		}
		total += p
	}
	return total
}

// nodeBufHint sums the hashed text lengths plus a fixed framing allowance so
// the encoder sizes its buffer once instead of growing it.
func nodeBufHint(n *knowledgev1.Node) int {
	const framingPerField = 5
	const nodeFieldCount = 14
	return bufHint(
		len(n.GetType()), len(n.GetSymbolName()), len(n.GetFilePath()),
		len(n.GetLanguage()), len(n.GetContent()), len(n.GetSignature()),
		len(n.GetTestKind()), len(n.GetDescription()), len(n.GetSource()),
		len(n.GetStatus()), nodeFieldCount*framingPerField, 32,
	)
}
