// SPDX-License-Identifier: Apache-2.0

// doctor_checks.go — the individual diagnostic checks behind
// `knowledge doctor`. Split from doctor.go for the 500-line cap; the
// command driver (runDoctor + glyphFor + the checkResult shape) stays
// in doctor.go, each check returns a checkResult here.

package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/fulminate-io/knowledge-mcp/internal/assets"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// checkServer reports on the local graph server. ok when the server is up and
// responding; warn when it's not — users on a remote backend should see that
// the local server is absent (it changes what `collect`/local search can do)
// even though it is a normal state for them, so it's a warn, not an err. Liveness
// is decided by the caller's single shared probe (defaultChecks) — this check
// never dials on its own.
func checkServer(gc *graphclient.GraphClient, port int, healthy bool) checkResult {
	if !healthy {
		detail := "run `knowledge start` to spawn it, or `brew services start knowledge` for a launchd-managed instance"
		if !serverBinaryInstalled() {
			detail = "knowledge-server binary not found — run `knowledge install` to download it, then `knowledge start`"
		}
		return checkResult{
			name:   "server",
			status: statusWarn,
			msg:    fmt.Sprintf("not running on port %d", port),
			detail: detail,
		}
	}
	status, err := gc.Status()
	if err != nil {
		return checkResult{
			name: "server", status: statusWarn,
			msg: fmt.Sprintf("listening on port %d but Status RPC failed: %v", port, err),
		}
	}
	return interpretServerStatus(port, status)
}

// interpretServerStatus turns a Status map (graphclient.GraphClient.Status
// shape) into the server check's running line. Pulled out of checkServer so
// the map-interpretation — including the summary_failed/embed_failed warn
// branch — is testable without a live server. Zero on both failure counters
// keeps the plain statusOK line; either non-zero warns and names the
// clear_llm_failures remediation tool.
func interpretServerStatus(port int, status map[string]any) checkResult {
	pid, _ := status["pid"].(float64)
	nodes, _ := status["nodes"].(float64)
	edges, _ := status["edges"].(float64)
	running := fmt.Sprintf("running on port %d (PID %d, %d nodes, %d edges)", port, int64(pid), int64(nodes), int64(edges))
	sf, _ := status["summary_failed"].(float64)
	ef, _ := status["embed_failed"].(float64)
	if sf > 0 || ef > 0 {
		return checkResult{
			name:   "server",
			status: statusWarn,
			msg:    fmt.Sprintf("%s — %d summary, %d embed failures", running, int64(sf), int64(ef)),
			detail: "run the `clear_llm_failures` tool to retry failed nodes once the provider is healthy",
		}
	}
	return checkResult{
		name:   "server",
		status: statusOK,
		msg:    running,
	}
}

// checkConfig loads + validates the config file for the summarizer
// consumer. Validation failure is a hard error since it'd block
// server startup too.
func checkConfig(configFile string) checkResult {
	path := configFile
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return checkResult{name: "config", status: statusErr, msg: "cannot resolve home dir: " + err.Error()}
		}
		path = filepath.Join(home, ".knowledge", "config")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return checkResult{
			name: "config", status: statusErr,
			msg: fmt.Sprintf("%s: %v", path, err),
		}
	}
	if err := cfg.Validate([]config.Consumer{config.ConsumerSummarizer, config.ConsumerDream, config.ConsumerHiveSupervisor}); err != nil {
		return checkResult{
			name: "config", status: statusErr,
			msg:    path,
			detail: err.Error(),
		}
	}
	sum, _ := cfg.Resolve(config.ConsumerSummarizer)
	dream, _ := cfg.Resolve(config.ConsumerDream)
	return checkResult{
		name: "config", status: statusOK,
		msg: fmt.Sprintf("%s valid (summarizer=%s/%s, dream=%s/%s)", path, sum.Provider, sum.Model, dream.Provider, dream.Model),
	}
}

// checkConsumerCLIs surfaces the cli_bin field state for every consumer
// whose resolved provider is a CLI provider — summarizer AND dream may
// run different CLI providers (e.g. summarizer=codex-cli, dream=claude-
// cli) with distinct cli_bin paths. One row per consumer, labeled by the
// actual provider so a codex-cli summarizer never prints a "claude-cli"
// row. API-provider consumers get an info row (no binary needed). The
// config check already validates cli_bin existence; this exposes the
// path per consumer for copy-paste troubleshooting.
func checkConsumerCLIs(configFile string) []checkResult {
	path := configFile
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".knowledge", "config")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return []checkResult{{name: "cli", status: statusInfo, msg: "config not loadable; see config check above"}}
	}
	consumers := []config.Consumer{config.ConsumerSummarizer, config.ConsumerDream, config.ConsumerHiveSupervisor}
	out := make([]checkResult, 0, len(consumers))
	for _, consumer := range consumers {
		out = append(out, checkConsumerCLI(cfg, consumer))
	}
	return out
}

// checkConsumerCLI resolves one consumer and reports its cli_bin state.
// Status semantics are preserved verbatim from the old single-consumer
// check: cli_bin unset / missing / a directory / non-executable each
// yield statusErr; an API provider yields statusInfo; a valid executable
// yields statusOK. The row name encodes consumer + actual provider.
func checkConsumerCLI(cfg *config.Config, consumer config.Consumer) checkResult {
	sec, err := cfg.Resolve(consumer)
	if err != nil {
		return checkResult{name: string(consumer) + "-cli", status: statusInfo, msg: consumer.String() + " section not resolvable"}
	}
	name := fmt.Sprintf("%s-cli (%s)", consumer, sec.Provider)
	if !sec.Provider.IsCLI() {
		return checkResult{name: name, status: statusInfo, msg: consumer.String() + " uses " + string(sec.Provider) + " (no CLI binary needed)"}
	}
	if sec.CLIBin == "" {
		return checkResult{
			name: name, status: statusErr,
			msg:    "cli_bin not set in config",
			detail: fmt.Sprintf("add `cli_bin = \"/absolute/path/to/%s\"` to [default] or [%s]", sec.Provider, consumer),
		}
	}
	info, err := os.Stat(sec.CLIBin)
	if err != nil {
		return checkResult{name: name, status: statusErr, msg: sec.CLIBin + " — " + err.Error()}
	}
	if info.IsDir() {
		return checkResult{name: name, status: statusErr, msg: sec.CLIBin + " is a directory, not an executable"}
	}
	if info.Mode()&0o111 == 0 {
		return checkResult{name: name, status: statusErr, msg: sec.CLIBin + " is not executable"}
	}
	return checkResult{name: name, status: statusOK, msg: sec.CLIBin + " (executable)"}
}

// checkVoyage reports Voyage key presence via the canonical resolver
// config.VoyageAPIKey() — [credentials].voyage_api_key first, then the
// VOYAGE_API_KEY env var. A config-only key (set in the file but not
// exported) correctly reports vector search ENABLED. Empty on both is
// the documented BM25-only mode — info-level, not a warning.
//
// VoyageAPIKey reads the loaded config singleton, so load the config
// here first (config.Load calls setActive). A load error is left for
// checkConfig to report; on error VoyageAPIKey falls back to env-only.
func checkVoyage(configFile string) checkResult {
	path := configFile
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".knowledge", "config")
	}
	_, _ = config.Load(path)
	if config.VoyageAPIKey() == "" {
		return checkResult{
			name: "voyage", status: statusInfo,
			msg: "VOYAGE_API_KEY unset — BM25-only search (no vector embeddings, no cross-encoder rerank)",
		}
	}
	return checkResult{
		name: "voyage", status: statusOK,
		msg: "VOYAGE_API_KEY set — vector embeddings + cross-encoder rerank enabled",
	}
}

// checkFulminateAuth checks for a stored OAuth refresh token in the
// credential store — the platform keychain when it is available, the
// ~/.knowledge/credentials file otherwise. Not-logged-in is info, not
// warning — paid features being unavailable is a fully-supported state.
func checkFulminateAuth() checkResult {
	store, err := auth.OpenStore()
	if err != nil {
		return checkResult{
			name: "fulminate", status: statusInfo,
			msg: "credential store unavailable: " + err.Error(),
		}
	}
	_, err = store.Get(context.Background(), auth.KeyRefreshToken)
	if errors.Is(err, auth.ErrNotFound) {
		return checkResult{
			name: "fulminate", status: statusInfo,
			msg:    "not logged in (paid features unavailable)",
			detail: "run `knowledge login` to authenticate",
		}
	}
	if err != nil {
		return checkResult{name: "fulminate", status: statusWarn, msg: "keychain read failed: " + err.Error()}
	}
	return checkResult{name: "fulminate", status: statusOK, msg: "logged in (refresh token present in keychain)"}
}

// checkClaudeAssets walks the embedded .claude/{agents,skills} tree
// and compares each file against the on-disk copy under ~/.claude/.
// Reports the count of missing or out-of-date files so the user
// knows whether to run `knowledge install-claude-assets`.
func checkClaudeAssets() checkResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return checkResult{name: "claude-assets", status: statusWarn, msg: "cannot resolve home dir: " + err.Error()}
	}
	dest := filepath.Join(home, ".claude")

	var missing, drift []string
	err = fs.WalkDir(assets.Files, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		embedded, err := assets.Files.ReadFile(p)
		if err != nil {
			return err
		}
		onDisk, err := os.ReadFile(filepath.Join(dest, p))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				missing = append(missing, p)
				return nil
			}
			return err
		}
		if hashEqual(embedded, onDisk) {
			return nil
		}
		drift = append(drift, p)
		return nil
	})
	if err != nil {
		return checkResult{name: "claude-assets", status: statusErr, msg: "walk failed: " + err.Error()}
	}
	if len(missing) == 0 && len(drift) == 0 {
		return checkResult{name: "claude-assets", status: statusOK, msg: "in sync with embedded version"}
	}
	sort.Strings(missing)
	sort.Strings(drift)
	return checkResult{
		name:   "claude-assets",
		status: statusWarn,
		msg:    fmt.Sprintf("%d missing, %d out of date", len(missing), len(drift)),
		detail: "run `knowledge install-claude-assets` to update",
	}
}

// checkClaudeMD reports whether the knowledge-managed block in
// ~/.claude/CLAUDE.md matches the embedded KNOWLEDGE_TOOLS.md reference.
// statusOK when the managed region equals assets.KnowledgeTools;
// statusWarn (with the install-claude-assets remediation) when it drifts
// or the file/markers are absent. Only the managed region is compared
// (via managedBlockInSync), so a user's own prose around the block never
// trips the warning.
func checkClaudeMD() checkResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return checkResult{name: "claude-md", status: statusWarn, msg: "cannot resolve home dir: " + err.Error()}
	}
	path := filepath.Join(home, ".claude", "CLAUDE.md")
	inSync, exists, err := managedBlockInSync(path, string(assets.KnowledgeTools))
	if err != nil {
		return checkResult{name: "claude-md", status: statusWarn, msg: err.Error()}
	}
	if !exists {
		return checkResult{
			name: "claude-md", status: statusWarn,
			msg:    "no knowledge-managed block in ~/.claude/CLAUDE.md",
			detail: "run `knowledge install-claude-assets` to prime it",
		}
	}
	if !inSync {
		return checkResult{
			name: "claude-md", status: statusWarn,
			msg:    "knowledge-managed block out of date",
			detail: "run `knowledge install-claude-assets` to update",
		}
	}
	return checkResult{name: "claude-md", status: statusOK, msg: "managed block in sync with embedded reference"}
}

// checkClaudeSettings reports whether the knowledge-managed promote-guard
// hook in ~/.claude/settings.json matches the embedded asset. statusOK when
// the managed entry equals assets.ClaudeHooks; statusWarn (with the
// install-claude-assets remediation) when it drifts or the file is absent.
// Only the managed entry is compared (via settingsInSync), so a user's own
// settings and other hooks never trip the warning. Mirrors checkClaudeMD.
func checkClaudeSettings() checkResult {
	home, err := os.UserHomeDir()
	if err != nil {
		return checkResult{name: "claude-settings", status: statusWarn, msg: "cannot resolve home dir: " + err.Error()}
	}
	path := filepath.Join(home, ".claude", "settings.json")
	inSync, exists, err := settingsInSync(path, assets.ClaudeHooks)
	if err != nil {
		return checkResult{name: "claude-settings", status: statusWarn, msg: err.Error()}
	}
	if !exists {
		return checkResult{
			name: "claude-settings", status: statusWarn,
			msg:    "no knowledge-managed collect-promote hook in ~/.claude/settings.json",
			detail: "run `knowledge install-claude-assets` to install it",
		}
	}
	if !inSync {
		return checkResult{
			name: "claude-settings", status: statusWarn,
			msg:    "knowledge-managed collect-promote hook out of date",
			detail: "run `knowledge install-claude-assets` to update",
		}
	}
	return checkResult{name: "claude-settings", status: statusOK, msg: "collect-promote hook in sync"}
}

// hashEqual returns true when the two byte slices have the same
// SHA-256 digest. Used by checkClaudeAssets to determine drift
// without doing a byte-by-byte compare on every file (the digest
// short-circuits unequal lengths quickly enough for this scale).
func hashEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	ha := sha256.Sum256(a)
	hb := sha256.Sum256(b)
	return hex.EncodeToString(ha[:]) == hex.EncodeToString(hb[:])
}
