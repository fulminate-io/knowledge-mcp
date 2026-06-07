// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// writeStubBin writes a shell script into t.TempDir(), marks it executable, and
// returns its absolute path. The script body is supplied by the caller so each
// test shapes the stub's behavior (echo a fixed envelope, exit non-zero, etc.).
func writeStubBin(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "stub-collector")
	script := "#!/bin/sh\n" + body + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700)) //nolint:gosec // test fixture must be executable
	return path
}

// TestRun_Transports covers both param transports: stdin delivers paramsJSON on
// stdin, flag delivers it as the named flag arg. Each stub echoes a valid
// envelope to prove the stdout JSON parses into a *Result.
func TestRun_Transports(t *testing.T) {
	envelope := `{"graph_type":"jira","graph_name":"board","nodes":[{"id":"a","type":"issue"}],"edges":[]}`

	t.Run("stdin", func(t *testing.T) {
		// The stub reads stdin into a node's metadata so we can prove the
		// params arrived on stdin, not as an arg. The param value is
		// quote-free so it embeds cleanly in the emitted JSON envelope.
		bin := writeStubBin(t, `IN=$(cat)
cat <<EOF
{"graph_type":"jira","graph_name":"board","nodes":[{"id":"a","type":"issue","metadata":{"got":"$IN"}}],"edges":[]}
EOF`)
		spec := &knowledgev1.CollectorSpec{BinaryPath: bin, ParamTransport: "stdin"}

		r, err := Run(context.Background(), spec, []byte(`repo=x`))
		require.NoError(t, err)
		require.NotNil(t, r)
		require.Len(t, r.Nodes, 1)
		assert.Equal(t, `repo=x`, r.Nodes[0].Metadata["got"])
	})

	t.Run("flag", func(t *testing.T) {
		// The stub echoes its --params arg ($2) into a node's metadata. The
		// param value is quote-free so it embeds cleanly in the JSON envelope.
		// metadata.flag captures $1 (must be --params) and metadata.got
		// captures $2 (the param value) so the named-flag transport is proven
		// in both the flag name AND the value position.
		bin := writeStubBin(t, `cat <<EOF
{"graph_type":"jira","graph_name":"board","nodes":[{"id":"a","type":"issue","metadata":{"flag":"$1","got":"$2"}}],"edges":[]}
EOF`)
		spec := &knowledgev1.CollectorSpec{BinaryPath: bin, ParamTransport: "flag:params"}

		r, err := Run(context.Background(), spec, []byte(`repo=y`))
		require.NoError(t, err)
		require.NotNil(t, r)
		require.Len(t, r.Nodes, 1)
		assert.Equal(t, `--params`, r.Nodes[0].Metadata["flag"])
		assert.Equal(t, `repo=y`, r.Nodes[0].Metadata["got"])
	})

	t.Run("plain stdout parses", func(t *testing.T) {
		bin := writeStubBin(t, "echo '"+envelope+"'")
		spec := &knowledgev1.CollectorSpec{BinaryPath: bin, ParamTransport: "stdin"}
		r, err := Run(context.Background(), spec, []byte(`{}`))
		require.NoError(t, err)
		require.NotNil(t, r)
		assert.Equal(t, "jira", r.GraphType)
	})
}

// TestRun_NonZeroExitSurfacesStderr confirms a non-zero exit fails loud and the
// returned error carries the binary's stderr verbatim.
func TestRun_NonZeroExitSurfacesStderr(t *testing.T) {
	bin := writeStubBin(t, `echo "boom: bad credentials" 1>&2
exit 3`)
	spec := &knowledgev1.CollectorSpec{BinaryPath: bin, ParamTransport: "stdin"}

	r, err := Run(context.Background(), spec, []byte(`{}`))
	require.Error(t, err)
	assert.Nil(t, r)
	assert.Contains(t, err.Error(), "boom: bad credentials")
}

// TestRun_MalformedJSONFailsLoud confirms a stub emitting non-JSON stdout
// returns a parse error rather than a silent empty Result.
func TestRun_MalformedJSONFailsLoud(t *testing.T) {
	bin := writeStubBin(t, `echo "this is not json"`)
	spec := &knowledgev1.CollectorSpec{BinaryPath: bin, ParamTransport: "stdin"}

	r, err := Run(context.Background(), spec, []byte(`{}`))
	require.Error(t, err)
	assert.Nil(t, r)
	assert.Contains(t, err.Error(), "malformed JSON")
}

// TestRun_RelativeBinaryPathRejected confirms the absolute-path guard fires
// before LookPath.
func TestRun_RelativeBinaryPathRejected(t *testing.T) {
	spec := &knowledgev1.CollectorSpec{BinaryPath: "relative-collector", ParamTransport: "stdin"}
	r, err := Run(context.Background(), spec, []byte(`{}`))
	require.Error(t, err)
	assert.Nil(t, r)
	assert.Contains(t, err.Error(), "must be absolute")
}

// TestRun_StdoutCapExceeded shrinks the cap via the package var and confirms a
// stub emitting more than the cap returns a non-nil exceeded error and a nil
// *Result (the over-cap output is never parsed).
func TestRun_StdoutCapExceeded(t *testing.T) {
	orig := maxStdoutBytes
	maxStdoutBytes = 16
	t.Cleanup(func() { maxStdoutBytes = orig })

	// Emit far more than 16 bytes of valid-looking JSON; the cap must trip
	// before the parse so the error is the exceeded error, not a parse error.
	bin := writeStubBin(t, `echo '{"graph_type":"jira","graph_name":"board","nodes":[],"edges":[]}'`)
	spec := &knowledgev1.CollectorSpec{BinaryPath: bin, ParamTransport: "stdin"}

	r, err := Run(context.Background(), spec, []byte(`{}`))
	require.Error(t, err)
	assert.Nil(t, r)
	assert.Contains(t, err.Error(), "exceeded")
}
