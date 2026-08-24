// SPDX-License-Identifier: Apache-2.0

package github

import "github.com/fulminate-io/knowledge-mcp/internal/postpopulate"

// init registers the "github" PostPopulate hook (OIDC federation).
func init() {
	postpopulate.Register("github", postpopulate.BreadthFamilyBroad, postPopulateOIDC)
}
