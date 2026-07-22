// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// handleRegisterRepo records a repo-name → absolute-checkout-path mapping in the
// machine-local manifest (~/.knowledge/repos.json) — the same registry a code
// `collect` populates. It is PURELY CLIENT-SIDE: the write lands on this
// machine's disk and no request is ever forwarded to the server, because the
// path is machine-specific and must never leave this host. Registering lets a
// cross-repo `ast` walk resolve a bare repo NAME to its real directory without
// having to `collect` the repo first.
//
// The op is idempotent by design: re-registering an existing name overwrites its
// recorded path, so a name can be re-pointed at a moved or renamed checkout.
func handleRegisterRepo(a manageArgs) kgtools.ToolResult {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return errorResult("manage(register_repo): name is required (the repo name to record)")
	}
	root := strings.TrimSpace(a.Root)
	if !filepath.IsAbs(root) {
		return errorResult(fmt.Sprintf("manage(register_repo): root %q must be an absolute checkout directory", root))
	}
	// Mirror resolveRepoDir's stat gate: the recorded path must be an existing
	// directory, so a typo is rejected at register time rather than silently
	// producing a manifest entry that later fails the walk-root stat-gate.
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return errorResult(fmt.Sprintf("manage(register_repo): root %q is not an existing directory", root))
	}
	// Surface a manifest-write failure — never echo success on a failed write.
	if err := recordRepoDir(name, root); err != nil {
		return errorResult("manage(register_repo): " + err.Error())
	}
	if a.Format == "json" {
		return jsonResult(map[string]string{"name": name, "root": root})
	}
	return textResult(fmt.Sprintf("Registered repo %q -> %s", name, root))
}
