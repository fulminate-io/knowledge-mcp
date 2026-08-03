// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHelpMutate_AdvertisesWritebackFields pins helpMutate's documentation of
// the pipeline writeback fields: keywords on mutate(update), and binary_vector
// with its 32-byte length contract on mutate(update_batch) items[] — the only
// path that carries an embedding. Relocated client-side alongside the help
// content.
//
// The companion wire-surface assertion (MutateToolDef advertises the
// property descriptions) lives on the server in
// cmd/knowledge-server/tools/tools_mutate_keywords_test.go since the
// schema struct still lives in the server package.
func TestHelpMutate_AdvertisesWritebackFields(t *testing.T) {
	for _, want := range []string{"keywords", "binary_vector", "32 bytes"} {
		assert.Contains(t, helpMutate, want, "helpMutate missing")
	}
}
