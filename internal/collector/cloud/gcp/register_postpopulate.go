// SPDX-License-Identifier: Apache-2.0

package gcp

import "github.com/fulminate-io/knowledge-mcp/internal/postpopulate"

// init registers the "gcp" PostPopulate hook.
func init() {
	postpopulate.Register("gcp", postpopulate.BreadthFamilyBroad, postPopulate)
}
