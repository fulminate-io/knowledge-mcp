// SPDX-License-Identifier: Apache-2.0

package docgen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpliceManagedBlock_ReplacesInPlace(t *testing.T) {
	content := "# Title\n\nintro prose\n\n" +
		"<!-- BEGIN GENERATED: params -->\nOLD BODY\n<!-- END GENERATED: params -->\n\n" +
		"trailing prose\n"

	out, err := spliceManagedBlock(content, "params", "NEW BODY")
	require.NoError(t, err)

	// Out-of-marker prose preserved byte-for-byte.
	assert.True(t, strings.HasPrefix(out, "# Title\n\nintro prose\n\n"), "leading prose must be preserved verbatim")
	assert.True(t, strings.HasSuffix(out, "\n\ntrailing prose\n"), "trailing prose must be preserved verbatim")
	// New body spliced between markers; old body gone.
	assert.Contains(t, out, "<!-- BEGIN GENERATED: params -->\nNEW BODY\n<!-- END GENERATED: params -->")
	assert.NotContains(t, out, "OLD BODY")
}

func TestSpliceManagedBlock_Idempotent(t *testing.T) {
	content := "before\n<!-- BEGIN GENERATED: x -->\nstale\n<!-- END GENERATED: x -->\nafter\n"

	once, err := spliceManagedBlock(content, "x", "BODY")
	require.NoError(t, err)
	twice, err := spliceManagedBlock(once, "x", "BODY")
	require.NoError(t, err)
	assert.Equal(t, once, twice, "re-splicing the same body must be a no-op")
}

func TestSpliceManagedBlock_MultipleNamedBlocks(t *testing.T) {
	content := "" +
		"<!-- BEGIN GENERATED: a -->\nold-a\n<!-- END GENERATED: a -->\n" +
		"between\n" +
		"<!-- BEGIN GENERATED: b -->\nold-b\n<!-- END GENERATED: b -->\n"

	out, err := spliceManagedBlock(content, "b", "new-b")
	require.NoError(t, err)
	// Block a untouched; block b replaced.
	assert.Contains(t, out, "<!-- BEGIN GENERATED: a -->\nold-a\n<!-- END GENERATED: a -->")
	assert.Contains(t, out, "<!-- BEGIN GENERATED: b -->\nnew-b\n<!-- END GENERATED: b -->")
	assert.NotContains(t, out, "old-b")
}

func TestSpliceManagedBlock_MissingBeginMarkerErrors(t *testing.T) {
	content := "prose only, no markers\n"
	out, err := spliceManagedBlock(content, "params", "BODY")
	require.Error(t, err, "missing BEGIN marker must return a non-nil error (loud failure, no silent append)")
	assert.Empty(t, out, "no content returned on error")
	assert.NotContains(t, err.Error(), "BODY")
	assert.Contains(t, err.Error(), "BEGIN marker")
}

func TestSpliceManagedBlock_MissingEndMarkerErrors(t *testing.T) {
	content := "<!-- BEGIN GENERATED: params -->\nbody but no end\n"
	out, err := spliceManagedBlock(content, "params", "BODY")
	require.Error(t, err, "missing END marker must return a non-nil error")
	assert.Empty(t, out)
	assert.Contains(t, err.Error(), "END marker")
}

func TestSpliceManagedBlock_EndBeforeBeginErrors(t *testing.T) {
	content := "<!-- END GENERATED: params -->\nstuff\n<!-- BEGIN GENERATED: params -->\n"
	_, err := spliceManagedBlock(content, "params", "BODY")
	require.Error(t, err, "END preceding BEGIN must return a non-nil error")
}
