// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadModulePath covers every go.mod shape the scan distinguishes, each
// with a DISTINCT concrete module path so no two expectations collapse onto one
// value.
func TestReadModulePath(t *testing.T) {
	cases := []struct {
		name    string
		gomod   string // "" means write no go.mod at all
		writeIt bool
		want    string
	}{
		{
			name:    "plain",
			gomod:   "module example.com/plain\n\ngo 1.24\n",
			writeIt: true,
			want:    "example.com/plain",
		},
		{
			name:    "quoted",
			gomod:   "module \"example.com/quoted\"\n\ngo 1.24\n",
			writeIt: true,
			want:    "example.com/quoted",
		},
		{
			name:    "trailing_comment",
			gomod:   "module example.com/commented // why this name\n\ngo 1.24\n",
			writeIt: true,
			want:    "example.com/commented",
		},
		{
			name:    "no_module_line",
			gomod:   "go 1.24\n\nrequire example.com/dep v1.2.3\n",
			writeIt: true,
			want:    "",
		},
		{
			name:    "absent_go_mod",
			writeIt: false,
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.writeIt {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(tc.gomod), 0o600))
			}

			got, err := ReadModulePath(dir)

			// THE NIL ERROR IS THE ASSERTION THAT MATTERS ON THE ABSENT CASE:
			// a non-Go repo is the normal case, not a read failure, and the
			// whole non-Go collect path depends on the distinction.
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
