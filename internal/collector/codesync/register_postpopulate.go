// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"github.com/fulminate-io/knowledge-mcp/internal/postpopulate"
)

// init installs the code-graph PostPopulate hook in the postpopulate registry.
// Runs when anything imports cmd/knowledge/internal/collector/codesync
// (client-side only; server does not pull codesync).
func init() {
	postpopulate.Register("code", codePostPopulate)
}
