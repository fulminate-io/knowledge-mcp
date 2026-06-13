// SPDX-License-Identifier: Apache-2.0

// doctor_deep.go — the opt-in `knowledge doctor --deep` reachability
// check. Split from doctor_checks.go so the default (cheap, side-effect-
// free) checks stay in one file and the network-touching deep check in
// another. Delegates to the synchronous precheck.RunAll, which fans out
// one ping per configured provider tuple plus Voyage concurrently.

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/llm/precheck"
)

// checkProvidersDeep exercises each configured consumer's provider path
// (login/reachability), not just cli_bin existence. It is only run when
// the user passes --deep — it makes real network/CLI calls. nil from
// precheck.RunAll means every provider is reachable; a joined error
// names every failure in one row's detail.
func checkProvidersDeep(configFile string) checkResult {
	path := configFile
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".knowledge", "config")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return checkResult{name: "providers", status: statusErr, msg: "config not loadable; see config check above"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	consumers := []config.Consumer{config.ConsumerSummarizer, config.ConsumerDream, config.ConsumerHiveSupervisor}
	if err := precheck.RunAll(ctx, cfg, consumers, config.VoyageAPIKey()); err != nil {
		return checkResult{
			name:   "providers",
			status: statusErr,
			msg:    "one or more configured providers unreachable",
			detail: err.Error(),
		}
	}
	return checkResult{name: "providers", status: statusOK, msg: "all configured providers reachable"}
}
