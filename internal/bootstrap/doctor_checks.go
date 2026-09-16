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
	path := doctorConfigPath(configFile)
	cfg, err := loadDoctorConfig(configFile)
	if err != nil {
		return checkResult{
			name: "config", status: statusErr,
			msg: fmt.Sprintf("%s: %v", path, err),
		}
	}
	if err := cfg.Validate([]config.Consumer{config.ConsumerSummarizer, config.ConsumerSupervisor}); err != nil {
		return checkResult{
			name: "config", status: statusErr,
			msg:    path,
			detail: err.Error(),
		}
	}
	sum, _ := cfg.Resolve(config.ConsumerSummarizer)
	return checkResult{
		name: "config", status: statusOK,
		msg: fmt.Sprintf("%s valid (summarizer=%s/%s)", path, sum.Provider, sum.Model),
	}
}

// checkConsumerCLIs surfaces the cli_bin field state for every consumer
// whose resolved provider is a CLI provider — summarizer AND supervisor may
// run different CLI providers (e.g. summarizer=codex-cli, supervisor=claude-
// cli) with distinct cli_bin paths. One row per consumer, labeled by the
// actual provider so a codex-cli summarizer never prints a "claude-cli"
// row. API-provider consumers get an info row (no binary needed). The
// config check already validates cli_bin existence; this exposes the
// path per consumer for copy-paste troubleshooting.
func checkConsumerCLIs(configFile string) []checkResult {
	path := configFile
	if path == "" {
		home, _ := bootstrapHomeDir()
		path = filepath.Join(home, ".knowledge", "config")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return []checkResult{{name: "cli", status: statusInfo, msg: "config not loadable; see config check above"}}
	}
	consumers := []config.Consumer{config.ConsumerSummarizer, config.ConsumerSupervisor}
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

// checkVoyage reports the embed and rerank axes SEPARATELY: each axis's
// resolved provider and whether that axis's own credential is present.
// The two axes are independently configurable ([embedder] and [reranker]),
// so one key report covering both would be wrong the moment an operator
// runs Voyage embeddings and a different rerank provider — the shared-key
// trap this ticket dissolves. The key is resolved FROM THE PROVIDER via
// config.APIKeyForEmbedProvider, which is the same resolution the runtime
// does, so a config-only key (set in the file but not exported) correctly
// reports its axis ENABLED. Both keys empty is the documented BM25-only
// mode — info-level, not a warning.
//
// The name stays "voyage" because it is the check's stable output label,
// and the function name is unchanged because eight landed criteria in
// other plans select its tests by name.
//
// The resolvers read the loaded config singleton, so load the config here
// first (config.Load calls setActive). A load error is left for
// checkConfig to report; on error the resolvers see no config and fall
// back to the default sections plus env-only credentials.
func checkVoyage(configFile string) checkResult {
	path := configFile
	if path == "" {
		home, _ := bootstrapHomeDir()
		path = filepath.Join(home, ".knowledge", "config")
	}
	_, _ = config.Load(path)

	var embSec config.EmbedSection
	var rrSec config.RerankSection
	if config.Loaded() {
		var err error
		if embSec, err = config.Active().ResolveEmbedder(); err != nil {
			return checkResult{name: "voyage", status: statusErr, msg: "[embedder] section is unusable: " + err.Error()}
		}
		if rrSec, err = config.Active().ResolveReranker(); err != nil {
			return checkResult{name: "voyage", status: statusErr, msg: "[reranker] section is unusable: " + err.Error()}
		}
	} else {
		embSec = config.EmbedSection{Provider: config.EmbedProviderVoyage}
		rrSec = config.RerankSection{Provider: config.EmbedProviderVoyage}
	}

	embOK, embClause := axisCredentialClause("embedder", embSec.Provider, embSec.ResolveEmbedKey(), "vector embeddings")
	_, rrClause := axisCredentialClause("reranker", rrSec.Provider, rrSec.ResolveRerankKey(), "cross-encoder rerank")

	if !embOK {
		return checkResult{
			name: "voyage", status: statusInfo,
			msg: "BM25-only search — " + embClause + "; " + rrClause,
		}
	}
	return checkResult{name: "voyage", status: statusOK, msg: embClause + "; " + rrClause}
}

// axisCredentialClause renders one axis's line of the doctor's key report
// and reports whether that axis is usable. A non-API provider (the
// deterministic fake) needs no credential and is usable without one.
//
// key is the ALREADY-RESOLVED credential for the axis — the caller applies
// the per-section-key-over-provider-key precedence — so the doctor reports
// exactly what the runtime would use rather than re-deriving it.
//
// The key's VALUE is never rendered. The clause reports presence only, and
// no caller has a reason to print more than that; a doctor report is
// pasted into issues.
func axisCredentialClause(axis string, provider config.EmbedProvider, key, feature string) (bool, string) {
	if !provider.IsAPI() {
		return true, fmt.Sprintf("%s %s: no credential required — %s enabled", axis, provider, feature)
	}
	if key == "" {
		return false, fmt.Sprintf("%s %s: no key — %s disabled", axis, provider, feature)
	}
	return true, fmt.Sprintf("%s %s: key set — %s enabled", axis, provider, feature)
}

// checkFulminateAuth checks for a stored OAuth refresh token in the
// credential store — the platform keychain when it is available, the
// ~/.knowledge/credentials file otherwise. Not-logged-in is info, not
// warning — paid features being unavailable is a fully-supported state.
//
// A malformed credential-namespace selector is NOT that state: it is bad
// configuration that blocks `serve` startup too, so it renders at statusErr,
// the same severity checkConfig uses for a config file that fails to validate.
// The two are told apart by the sentinel rather than by the message, because
// the message is prose and will be reworded.
func checkFulminateAuth() checkResult {
	store, err := auth.OpenStore()
	if errors.Is(err, auth.ErrCredentialNamespaceInvalid) {
		return checkResult{name: "fulminate", status: statusErr, msg: err.Error()}
	}
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
	home, err := bootstrapHomeDir()
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
	home, err := bootstrapHomeDir()
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

// checkClaudeSettings reports whether the knowledge-managed PreToolUse hooks in
// ~/.claude/settings.json match the embedded asset. statusOK when both managed
// entries equal the rendered assets.ClaudeHooks; statusWarn (with the
// install-claude-assets remediation) when they drift or the file is absent.
// Only the managed entries are compared (via settingsInSync), so a user's own
// settings and other hooks never trip the warning. Mirrors checkClaudeMD.
//
// THE COMPARISON IS RENDERED AT THE PORT THE FILE ITSELF NAMES, read back out
// of the installed session hook's url. The asset is port-rendered now, and
// comparing against a default-port rendering would warn permanently for every
// user who installed on another port — which is a user-visible regression, not
// a drift report. The port is a per-install parameter; the SHAPE is what drift
// means. With no entry installed there is no port to read and the default is
// used, which is the honest comparison for a file that has none.
func checkClaudeSettings(mcpPort int, mcpPortKnown bool) checkResult {
	home, err := bootstrapHomeDir()
	if err != nil {
		return checkResult{name: "claude-settings", status: statusWarn, msg: "cannot resolve home dir: " + err.Error()}
	}
	path := filepath.Join(home, ".claude", "settings.json")
	renderPort, installedPort, portKnown := claudeHookRenderPort(path, mcpPort, mcpPortKnown)
	hookEntries, err := renderClaudeHooks(assets.ClaudeHooks, renderPort)
	if err != nil {
		return checkResult{name: "claude-settings", status: statusWarn, msg: err.Error()}
	}
	inSync, exists, err := settingsInSync(path, hookEntries)
	if err != nil {
		return checkResult{name: "claude-settings", status: statusWarn, msg: err.Error()}
	}
	return claudeSettingsResult(inSync, exists, portKnown && mcpPortKnown, installedPort, mcpPort)
}

// claudeHookRenderPort resolves the port the SHAPE comparison renders at, plus
// the port the installed hook names and whether one was found.
//
// The shape is always compared at the installed port when there is one, so a
// user on any port is never told their hooks drifted merely because the port
// differs. With nothing installed there is no port to read: the caller's port
// is used when it KNOWS one, and the documented default otherwise — and in that
// case there is no managed entry for the comparison to be wrong about.
func claudeHookRenderPort(path string, mcpPort int, mcpPortKnown bool) (renderPort, installedPort int, portKnown bool) {
	renderPort = graphclient.DefaultMCPHTTPPort
	if mcpPortKnown {
		renderPort = mcpPort
	}
	if data, readErr := os.ReadFile(path); readErr == nil { //nolint:gosec // the user's own ~/.claude/settings.json, resolved through bootstrapHomeDir
		if installed, ok := claudeHookPortFromSettings(data); ok {
			return installed, installed, true
		}
	}
	return renderPort, 0, false
}

// claudeSettingsResult maps the check's observations onto the rendered result,
// naming the remediation on every warning arm.
//
// THE PORT IS TWO QUESTIONS, not one. Rendering the comparison at the port the
// FILE names keeps a user on a non-default --mcp-port from being told their
// hooks drifted when only the port differs; but a hook whose port no daemon
// serves delivers nowhere, and that is exactly what this diagnostic exists to
// catch. So the shape is compared at the installed port AND the installed port
// is compared to the port this daemon actually serves on, as a separate arm
// that names both numbers.
//
// comparable is FALSE unless BOTH ports are known — the installed one read off
// the file, the served one supplied by a caller that actually knows it. A
// caller that does not know the served port gets the shape verdict and no port
// verdict, because comparing against a default would warn on every correctly
// installed non-default hook and tell the user to reinstall it at a port
// nothing serves.
func claudeSettingsResult(inSync, exists, comparable bool, installedPort, mcpPort int) checkResult {
	switch {
	case !exists:
		return checkResult{
			name: "claude-settings", status: statusWarn,
			msg:    "no knowledge-managed hooks in ~/.claude/settings.json",
			detail: "run `knowledge install-claude-assets` to install the collect-promote and session hooks",
		}
	case !inSync:
		return checkResult{
			name: "claude-settings", status: statusWarn,
			msg:    "knowledge-managed hooks out of date",
			detail: "run `knowledge install-claude-assets` to update",
		}
	case comparable && installedPort != mcpPort:
		return checkResult{
			name: "claude-settings", status: statusWarn,
			msg: fmt.Sprintf("the session hook posts to port %d but this daemon serves MCP on %d — deliveries reach nothing and sessions resolve to none",
				installedPort, mcpPort),
			detail: fmt.Sprintf("run `knowledge install-claude-assets --mcp-port %d`", mcpPort),
		}
	default:
		return checkResult{name: "claude-settings", status: statusOK, msg: "collect-promote + session hooks in sync"}
	}
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
