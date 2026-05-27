// SPDX-License-Identifier: Apache-2.0

package k8s

import "github.com/fulminate-io/knowledge-mcp/internal/postpopulate"

// init registers the "k8s" PostPopulate hook.
func init() {
	postpopulate.Register("k8s", postPopulate)
}
