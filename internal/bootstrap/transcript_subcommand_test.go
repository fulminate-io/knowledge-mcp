// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRunSubcommand_RoutesTranscriptUpload asserts os.Args[1]=="transcript-upload"
// dispatches to cli.TranscriptUploadCmd. `--help` keeps it fully offline (no
// keychain / network) while still exercising the routing + clean exit.
func TestRunSubcommand_RoutesTranscriptUpload(t *testing.T) {
	prevArgs := os.Args
	t.Cleanup(func() { os.Args = prevArgs })
	os.Args = []string{"knowledge", "transcript-upload", "--help"}

	handled, code := RunSubcommand()
	assert.True(t, handled, "transcript-upload is a recognized subcommand")
	assert.Equal(t, 0, code, "--help exits 0")
}
