// SPDX-License-Identifier: Apache-2.0

package recipe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifest_RoundTrip(t *testing.T) {
	out := FormatSourceManifest("hohpe-eip", "eip-to-design-patterns")
	assert.Equal(t, "source=hohpe-eip;recipe=eip-to-design-patterns", out)

	src, rec, err := ParseSourceManifest(out)
	require.NoError(t, err)
	assert.Equal(t, "hohpe-eip", src)
	assert.Equal(t, "eip-to-design-patterns", rec)
}

func TestManifest_ParseErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		msg  string
	}{
		{"empty", "", "empty SourceManifest"},
		{"malformed segment", "source", "expected key=value"},
		{"missing source", "recipe=x", "missing"},
		{"missing recipe", "source=x", "missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseSourceManifest(tc.in)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
}

func TestManifest_IgnoresUnknownKeys(t *testing.T) {
	src, rec, err := ParseSourceManifest("source=s;format=v2;recipe=r")
	require.NoError(t, err)
	assert.Equal(t, "s", src)
	assert.Equal(t, "r", rec)
}
