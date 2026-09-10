// SPDX-License-Identifier: Apache-2.0

// collector_config_scopes.go — where the daemon looks for the two custom-collector
// config files, and the loader every consumer in this package builds from them.
//
// THE USER PATH IS RESOLVED ONCE AT PACKAGE INIT and the project path once per
// call, because the two answer different questions: the user scope is a property
// of the MACHINE the daemon runs on, while the project scope is a property of the
// SESSION making the call. Both are handed to the loader as parameters — the
// loader itself resolves no home directory and has no $HOME fallback, so a test
// building its scopes under t.TempDir() cannot reach the operator's real
// ~/.knowledge no matter what the process environment says.

package tools

import (
	"context"
	"fmt"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/collectorconfig"
)

// collectorUserConfigPath is ~/.knowledge/collectors.json, resolved at init like
// every other ~/.knowledge consumer in this binary. Tests swap it for a path
// under t.TempDir(), which is the same mechanism the repo manifest uses.
//
// collectorUserConfigErr holds the home-resolution failure when there is one. An
// unresolvable home is reported LOUDLY at the lookup rather than degraded into
// "the user scope has no entries": every collector an operator installed would
// silently stop being registered.
var collectorUserConfigPath, collectorUserConfigErr = defaultCollectorUserConfigPath()

// defaultCollectorUserConfigPath resolves the user-scope file.
func defaultCollectorUserConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("custom collector config: the home directory could not be resolved, so the user-scope file %s cannot be located: %w",
			collectorconfig.UserPathIn("<home>"), err)
	}
	if home == "" {
		return "", fmt.Errorf("custom collector config: the home directory resolved empty, so the user-scope file %s cannot be located",
			collectorconfig.UserPathIn("<home>"))
	}
	return collectorconfig.UserPathIn(home), nil
}

// collectorLoader builds the loader for THIS call: the machine's user scope plus
// the project scope of the session's own working directory.
//
// THE PROJECT SCOPE IS RESOLVED FROM THE SESSION, NOT FROM THE PROCESS. The
// registration lookup runs inside the shared daemon, so "which repository" is
// answered by the per-session workspace cwd carried on ctx, falling back to the
// process-global --root, then walked UP to the nearest ancestor holding a
// project-scope file. Two concurrent sessions standing in two different
// repositories legitimately see two different project files, and a session
// carrying no workspace cwd sees the user scope alone.
func collectorLoader(ctx context.Context, deps ClientDeps) (collectorconfig.Loader, error) {
	if collectorUserConfigErr != nil {
		return collectorconfig.Loader{}, collectorUserConfigErr
	}
	return collectorconfig.Loader{
		UserPath:    collectorUserConfigPath,
		ProjectPath: collectorconfig.FindProjectPath(effectiveCwd(ctx, deps)),
	}, nil
}
