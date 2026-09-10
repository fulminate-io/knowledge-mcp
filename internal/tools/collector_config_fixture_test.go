// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// collector_config_fixture_test.go — the scoped config files every collect test
// in this package registers its families through, built under t.TempDir().
//
// NO TEST HERE READS OR WRITES THE OPERATOR'S ~/.knowledge. The user-scope path
// is a package variable resolved once at init; TestMain repoints it at a scratch
// directory for the whole suite (see neutralizeCollectorUserScope), and each test
// that needs entries repoints it again at its own. A suite that read the real
// home file would answer differently on every machine and would write an
// operator's registrations on a bad day.

// namedEntry pairs a family name with its config entry, which is what the file
// stores and what these fixtures pass around.
type namedEntry struct {
	name  string
	entry collectorconfig.Entry
}

// collectorScope is a temp-directory config file a test writes entries into.
type collectorScope struct{ path string }

// useTempCollectorScope points the USER scope at a fresh file under t.TempDir()
// and writes the given entries into it, restoring the package variable when the
// test ends.
func useTempCollectorScope(t *testing.T, entries ...namedEntry) *collectorScope {
	t.Helper()
	dir := t.TempDir()
	scope := &collectorScope{path: filepath.Join(dir, ".knowledge", collectorconfig.FileName)}
	prevPath, prevErr := collectorUserConfigPath, collectorUserConfigErr
	collectorUserConfigPath, collectorUserConfigErr = scope.path, nil
	t.Cleanup(func() { collectorUserConfigPath, collectorUserConfigErr = prevPath, prevErr })
	scope.write(t, entries...)
	return scope
}

// write replaces the file's contents with exactly these entries. Rewriting the
// whole file is how a test drives a HAND EDIT: the loader re-reads from disk on
// every lookup, so the next collect sees whatever this leaves behind.
func (s *collectorScope) write(t *testing.T, entries ...namedEntry) {
	t.Helper()
	m := make(map[string]collectorconfig.Entry, len(entries))
	for _, e := range entries {
		m[e.name] = e.entry
	}
	require.NoError(t, collectorconfig.Write(s.path, m))
}

// writeRaw replaces the file's contents with arbitrary bytes, for the refusal
// rows that need a file the loader cannot read.
func (s *collectorScope) writeRaw(t *testing.T, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(s.path), 0o750))
	require.NoError(t, os.WriteFile(s.path, []byte(body), 0o600))
}

// neutralizeCollectorUserScope points the user-scope path at a scratch file that
// does not exist, for the WHOLE suite. It is called from TestMain so a test that
// never thinks about collectors still cannot reach the operator's home.
func neutralizeCollectorUserScope() func() {
	dir, err := os.MkdirTemp("", "kn-collector-scope-*")
	if err != nil {
		panic("neutralizing the collector user scope for the test suite: " + err.Error())
	}
	prevPath, prevErr := collectorUserConfigPath, collectorUserConfigErr
	collectorUserConfigPath, collectorUserConfigErr = filepath.Join(dir, collectorconfig.FileName), nil
	return func() {
		collectorUserConfigPath, collectorUserConfigErr = prevPath, prevErr
		_ = os.RemoveAll(dir)
	}
}
