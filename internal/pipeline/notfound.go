// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"errors"

	"connectrpc.com/connect"
)

// isGraphNotFound reports whether err unwraps to a *connect.Error carrying
// connect.CodeNotFound.
//
// It is an explicit SIBLING of rateLimitHint (ratelimit.go), which classifies the
// same errors for the opposite question, and follows its shape.
//
// IT UNWRAPS RATHER THAN TYPE-ASSERTS. The scan seam wraps with %w, so a bare
// type assertion would miss every production error and this classifier would
// silently never fire.
//
// WHY THE CLASSIFICATION IS SAFE HERE AND NOWHERE ELSE. PipelineScan is
// per-graph, and the server maps a failed Scope of the requested graph — and
// nothing else — to CodeNotFound, so the whole request named one graph and the
// answer was that the graph is not there. That is unambiguous. It is deliberately
// NOT taken on a routed Execute, where CodeNotFound ALSO means a missing NODE:
// the by-id path treats that code as an ordinary miss, and evicting the graph
// holding the node would be a defect, not a repair.
func isGraphNotFound(err error) bool {
	if err == nil {
		return false
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return false
	}
	return ce.Code() == connect.CodeNotFound
}
