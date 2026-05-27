// SPDX-License-Identifier: Apache-2.0

package aws

import "github.com/fulminate-io/knowledge-mcp/internal/postpopulate"

// init registers the "aws" PostPopulate hook. Blank-imported on the
// client; the server blank-imports the SDK-free relocation (Phase 6.3).
func init() {
	postpopulate.Register("aws", postPopulate)
}
