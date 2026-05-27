// SPDX-License-Identifier: Apache-2.0

package azure

import "github.com/fulminate-io/knowledge-mcp/internal/postpopulate"

// init registers the "azure" PostPopulate hook.
func init() {
	postpopulate.Register("azure", postPopulate)
}
