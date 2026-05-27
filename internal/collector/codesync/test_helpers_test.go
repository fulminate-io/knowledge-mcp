// SPDX-License-Identifier: Apache-2.0

package codesync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestCtx returns a plain background context. CodeCollector.Collect runs
// the pure parser/RTA collection path (parser.Populate + augmentWithPrecise
// CallGraph + DetectBranch + go.mod/layer reads) — none of which read a store
// txn from the context — so the former store.NewTxn/WithTxn wrapper was
// vestigial. The client is wire-only: tests never spin up a store engine.
func newTestCtx(t testing.TB) context.Context {
	t.Helper()
	return context.Background()
}

// writeFile creates a file at path with content, creating intermediate
// directories as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
