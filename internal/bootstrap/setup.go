// SPDX-License-Identifier: Apache-2.0

// setup.go — `knowledge setup`: the one-command install/update flow the
// install.sh bootstrap and a bare `knowledge setup` both drive. It owns
// everything interactive/stateful: first-run vs update detection,
// guided (interactive) vs headless (env-sourced) config writing, the
// binary self-update leg, the conditional Claude/Codex asset install,
// and the persistence-unit + restart tail (Phase 4).
//
// Behavior-only flags — NO --version (install.sh consumes that for its
// own pinned download and never forwards it) and NO secret flags
// anywhere (credentials come from the process env or the config file,
// never argv). CLI mode: status to stdout, errors to stderr.

package bootstrap

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// setupFlags holds the parsed `knowledge setup` flags. Every flag is a
// behavior toggle — there is deliberately no --version and no
// *api-key*/*token*/*secret* flag: secrets live in the environment or
// ~/.knowledge/config, never argv.
type setupFlags struct {
	headless     bool
	reconfigure  bool
	noClaude     bool
	noCodex      bool
	noService    bool
	noMCP        bool
	noSelfUpdate bool
}

// registerSetupFlags binds the behavior flags into f. Pure register-only
// seam (no fs.Parse) so setup_test.go can build the real FlagSet and
// assert the exact install.sh-forwarded arg set parses cleanly.
func registerSetupFlags(fs *flag.FlagSet, f *setupFlags) {
	fs.BoolVar(&f.headless, "headless", false, "Non-interactive: no prompts; credentials sourced from the environment")
	fs.BoolVar(&f.reconfigure, "reconfigure", false, "Rewrite an existing config from the template (default: leave it untouched)")
	fs.BoolVar(&f.noClaude, "no-claude", false, "Skip installing Claude Code assets even when `claude` is on PATH")
	fs.BoolVar(&f.noCodex, "no-codex", false, "Skip installing Codex assets even when `codex` is on PATH")
	fs.BoolVar(&f.noService, "no-service", false, "Skip installing persistence units and restarting the daemons")
	fs.BoolVar(&f.noMCP, "no-mcp", false, "Skip registering the knowledge MCP server with the detected CLI")
	fs.BoolVar(&f.noSelfUpdate, "no-self-update", false, "Skip the binary self-update leg (set by install.sh, which just installed the binaries)")
}

// selfUpdate is the package-level indirection over runInstall so
// setup_test.go can substitute a spy that never hits the network.
var selfUpdate = runInstall

// claudeAssetsFn / codexAssetsFn are package-level indirections over the
// real asset-installer entry points so setup_test.go can spy on
// invocation + the curated arg slice without running the installer
// internals (same seam pattern as selfUpdate).
var (
	claudeAssetsFn = runInstallClaudeAssets
	codexAssetsFn  = runInstallCodexAssets
)

// stdinIsTTY reports whether stdin is an interactive terminal (a
// character device). Seam so setup_test.go can force the non-TTY
// headless path deterministically (CI stdin is already non-TTY).
var stdinIsTTY = func() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// setupLoopbackAddr is the bindAddr handed to config.AutoDetect for the
// provider default: a loopback addr selects the local precedence (CLI
// providers eligible), matching a developer/desktop install.
func setupLoopbackAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 15022}
}

// reducedAccuracyWarning is printed when no Voyage key resolves — the
// written config runs BM25-only (no embeddings/rerank).
const reducedAccuracyWarning = "knowledge setup: no Voyage API key set — search runs in BM25-only mode (no embeddings or rerank). Set VOYAGE_API_KEY or add voyage_api_key to ~/.knowledge/config for full-accuracy semantic search."

// customizationLossWarning is printed on --reconfigure when the config
// carries hand-edited sections the template regenerate would drop.
const customizationLossWarning = "knowledge setup: --reconfigure regenerates ~/.knowledge/config from the template — any hand-edited [summarizer]/[supervisor]/[topics] sections or health_probe_interval will be lost and must be re-applied by hand."

// noProviderNote is printed when setup finds NO LLM provider (no
// claude/codex CLI on PATH, no ANTHROPIC/OPENAI/GEMINI key). Setup still
// COMPLETES — it writes an unconfigured (BM25-only) config, installs
// units, and brings the daemon up: the degrade-not-die invariant. The
// note tells the user exactly how to enable the disabled features later.
const noProviderNote = "knowledge setup: no LLM provider detected (no claude/codex CLI on PATH, no ANTHROPIC_API_KEY/OPENAI_API_KEY/GEMINI_API_KEY). Proceeding with an unconfigured, BM25-only setup — the daemon starts and keyword search works, but summarization and semantic search are disabled. To enable them: install the claude CLI (or set one of the API keys), then re-run `knowledge setup`."

// runSetup is the entry point dispatched from RunSubcommand. Parses the
// behavior flags, decides first-run vs update, writes config when
// appropriate, self-updates the binaries, installs CLI assets, and runs
// the persistence/restart tail.
func runSetup(args []string) error {
	fs := flag.NewFlagSet("knowledge setup", flag.ContinueOnError)
	var f setupFlags
	registerSetupFlags(fs, &f)
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Non-TTY stdin forces headless — a piped `curl | sh` or a
	// redirected stdin cannot answer prompts.
	headless := f.headless || !stdinIsTTY()

	cfgPath, err := defaultConfigPath()
	if err != nil {
		return fmt.Errorf("knowledge setup: resolve config path: %w", err)
	}
	configPresent, err := configExists(cfgPath)
	if err != nil {
		return err
	}

	// Config write runs on first-run (absent) OR --reconfigure. Update
	// mode (present + no --reconfigure) leaves the config byte-identical.
	if !configPresent || f.reconfigure {
		proceed, werr := writeSetupConfig(cfgPath, configPresent, headless, &f)
		if werr != nil {
			return werr
		}
		if !proceed {
			// User declined the reconfigure customization-loss confirm;
			// config left byte-identical, nothing further to do.
			return nil
		}
	}

	// Binary self-update leg — skipped on the install.sh handoff
	// (--no-self-update), where the running client IS the just-installed
	// release so its compiled-in bootstrap.Version is the restart target.
	targetVersion := Version
	if !f.noSelfUpdate {
		installedTag, uerr := selfUpdate(nil)
		if uerr != nil {
			return fmt.Errorf("knowledge setup: self-update binaries: %w", uerr)
		}
		if installedTag != "" {
			targetVersion = installedTag
		}
	}

	// Conditional Claude/Codex asset install (silent skip when the CLI
	// is absent or gated off).
	if err := installSetupAssets(&f); err != nil {
		return err
	}

	// Persistence units + restart tail. --no-service short-circuits
	// before any unit write or restart.
	if f.noService {
		return nil
	}
	return installServiceUnitsAndRestart(targetVersion)
}

// configExists stats cfgPath and reports whether a config already lives
// there. A stat error other than not-exist is fatal (permission /
// broken symlink) — mirrors ensureFileExists.
func configExists(cfgPath string) (bool, error) {
	_, err := os.Stat(cfgPath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("knowledge setup: stat %s: %w", cfgPath, err)
}

// writeSetupConfig routes to the headless (env-sourced, zero prompts) or
// guided (interactive) config-write path. It returns proceed=false ONLY
// when the interactive --reconfigure customization-loss confirm is
// declined — the config is left byte-identical and the caller stops.
func writeSetupConfig(cfgPath string, existing, headless bool, _ *setupFlags) (proceed bool, err error) {
	if headless {
		return true, writeHeadlessConfig(cfgPath, existing)
	}
	return writeGuidedConfig(cfgPath, existing)
}

// writeGuidedConfig gathers config interactively and writes it. On a
// --reconfigure over an existing config it pre-loads stored credentials
// (empty prompt input preserves each) and, when the config carries
// hand-edited sections beyond [default]/[credentials], warns that the
// rewrite regenerates from the template and REQUIRES a y/n confirm
// (default no) — a no/empty answer aborts (proceed=false), leaving the
// config byte-identical.
func writeGuidedConfig(cfgPath string, existing bool) (proceed bool, err error) {
	scanner := bufio.NewScanner(os.Stdin)

	var stored config.Credentials
	if existing {
		var keep bool
		stored, keep, err = guidedReconfigurePreload(cfgPath, scanner)
		if err != nil {
			return false, err
		}
		if !keep {
			return false, nil
		}
	}

	detected, derr := config.AutoDetect(setupLoopbackAddr())
	if derr != nil {
		if !errors.Is(derr, config.ErrNoProvider) {
			return false, fmt.Errorf("knowledge setup: auto-detect provider: %w", derr)
		}
		// Degrade-not-die: no provider detected → present the provider list
		// with NO preselection (detected stays zero-value, so the prompt
		// default is empty). The user picks one, or skips (empty input) →
		// unconfigured, BM25-only config. Setup still completes.
		fmt.Fprintln(os.Stdout, noProviderNote)
		detected = config.DetectedProvider{}
	}
	chosen := resolveChosenProvider(scanner, detected)

	// Credential prompts default to the stored value so EMPTY input
	// (skip) PRESERVES a stored key instead of clearing it.
	voyage := promptLine(scanner, "Voyage API key (embeddings + rerank; empty → BM25-only search)", stored.VoyageAPIKey)
	if voyage == "" {
		fmt.Fprintln(os.Stdout, reducedAccuracyWarning)
	}
	linear := promptLine(scanner, "Linear API key (empty → Linear backend disabled)", stored.LinearAPIKey)

	// Start from stored so anthropic/openai/gemini keys (not prompted
	// here) are preserved; overwrite only the two prompted keys.
	creds := stored
	creds.VoyageAPIKey = voyage
	creds.LinearAPIKey = linear

	if err := renderAndWriteConfig(cfgPath, chosen, creds); err != nil {
		return false, err
	}
	return true, nil
}

// guidedReconfigurePreload loads an existing config on the --reconfigure
// guided path: it returns the stored credentials (so empty prompt input
// preserves each) and, when the config carries hand-edited sections
// beyond [default]/[credentials], warns + requires a y/n confirm. A
// no/empty answer returns proceed=false (the caller aborts, leaving the
// config byte-identical).
func guidedReconfigurePreload(cfgPath string, scanner *bufio.Scanner) (stored config.Credentials, proceed bool, err error) {
	cfg, lerr := config.Load(cfgPath)
	if lerr != nil {
		return stored, false, fmt.Errorf("knowledge setup: load existing config: %w", lerr)
	}
	if cfg.Credentials != nil {
		stored = *cfg.Credentials
	}
	if hasCustomSections(cfg) {
		fmt.Fprintln(os.Stdout, customizationLossWarning)
		if !promptYesNo(scanner, "Regenerate config from the template and lose those customizations?", false) {
			fmt.Fprintln(os.Stdout, "knowledge setup: reconfigure aborted; config left unchanged")
			return stored, false, nil
		}
	}
	return stored, true, nil
}

// writeHeadlessConfig writes the config with ZERO prompts. First-run:
// AutoDetect the provider and PERSIST any set credential env vars into
// [credentials] — a unit-managed daemon runs with a sanitized env, so a
// key provided only via env would never reach it; persisting is the only
// path that makes the just-installed daemon functional. On a
// --reconfigure re-run over an existing config, existing
// [credentials] values WIN: env fills only the keys the config leaves
// unset — a differing env value never overwrites a stored secret.
func writeHeadlessConfig(cfgPath string, existing bool) error {
	var stored config.Credentials
	var existingCfg *config.Config
	if existing {
		cfg, lerr := config.Load(cfgPath)
		if lerr != nil {
			return fmt.Errorf("knowledge setup: load existing config: %w", lerr)
		}
		if cfg.Credentials != nil {
			stored = *cfg.Credentials
		}
		existingCfg = cfg
	}

	detected, derr := config.AutoDetect(setupLoopbackAddr())
	if derr != nil {
		if !errors.Is(derr, config.ErrNoProvider) {
			return fmt.Errorf("knowledge setup: auto-detect provider: %w", derr)
		}
		// Degrade-not-die: no provider on this box → write an unconfigured
		// (BM25-only) starter so the daemon still boots. detected stays
		// zero-value, which RenderStarter emits as the commented [default]
		// guidance block. Any env-provided credential keys are still
		// persisted below so a later `knowledge setup` (with a provider
		// installed) has them on hand.
		fmt.Fprintln(os.Stdout, noProviderNote)
		detected = config.DetectedProvider{}
	}

	// Existing config wins; env fills only unset keys. Keys with no env
	// var stay unset in the file (runtime still falls back to env).
	creds := config.Credentials{
		VoyageAPIKey:    coalesceCred(stored.VoyageAPIKey, "VOYAGE_API_KEY"),
		LinearAPIKey:    coalesceCred(stored.LinearAPIKey, "LINEAR_API_KEY"),
		AnthropicAPIKey: coalesceCred(stored.AnthropicAPIKey, "ANTHROPIC_API_KEY"),
		OpenAIAPIKey:    coalesceCred(stored.OpenAIAPIKey, "OPENAI_API_KEY"),
		GeminiAPIKey:    coalesceCred(stored.GeminiAPIKey, "GEMINI_API_KEY"),
	}

	if creds.VoyageAPIKey == "" {
		fmt.Fprintln(os.Stdout, reducedAccuracyWarning)
	}
	// Headless cannot prompt, so the customization-loss case warns and
	// PROCEEDS (no confirmation, no stdin read, no hang).
	if existingCfg != nil && hasCustomSections(existingCfg) {
		fmt.Fprintln(os.Stdout, customizationLossWarning)
	}

	return renderAndWriteConfig(cfgPath, detected, creds)
}

// resolveChosenProvider prompts for the provider (default = detected)
// and returns the DetectedProvider to render. Keeping the detected
// provider reuses its resolved model + cli_bin verbatim; overriding it
// looks up that provider's default seed model (cli_bin is left for the
// user to fill, as the starter documents).
func resolveChosenProvider(scanner *bufio.Scanner, detected config.DetectedProvider) config.DetectedProvider {
	entered := promptLine(scanner, "LLM provider (anthropic/openai/gemini/claude-cli/codex-cli)", string(detected.Provider))
	if config.Provider(entered) == detected.Provider {
		return detected
	}
	p := config.Provider(entered)
	return config.DetectedProvider{Provider: p, Model: config.DefaultModelFor(p)}
}

// renderAndWriteConfig renders the starter for the chosen provider +
// credentials and writes it with the ensureFileExists shape verbatim:
// os.MkdirAll(dir, 0o750) then os.WriteFile(path, body, 0o600). Same
// perms, same order — no divergent write shape.
//
// This function REWRITES THE WHOLE FILE, so it is the second of the client's
// two writers that can clobber an existing fulminate_account_id (the other,
// config.WriteSelectedAccountID, only ever sets a non-empty value). The
// starter template structurally cannot carry the key, so the selection is
// read BEFORE the overwrite and re-applied immediately after: without that,
// `knowledge setup --reconfigure` would silently erase the selection and the
// next cloud call would be rerouted to the caller's primary account.
//
// Read-before-write is load-bearing — the read must happen while the old file
// is still on disk. Between the write and the re-apply the file is briefly
// present without the entry; a crash in that window leaves the pre-existing
// legal unset state (no header, gateway resolves as before), recoverable with
// one `knowledge account use`, not a corrupt file.
func renderAndWriteConfig(cfgPath string, detected config.DetectedProvider, creds config.Credentials) error {
	// "" when the file is absent or holds no selection.
	prior, _ := config.ReadSelectedAccountID(cfgPath)

	body, err := config.RenderStarter(detected, creds)
	if err != nil {
		return fmt.Errorf("knowledge setup: render config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o750); err != nil {
		return fmt.Errorf("knowledge setup: mkdir %s: %w", filepath.Dir(cfgPath), err)
	}
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		return fmt.Errorf("knowledge setup: write %s: %w", cfgPath, err)
	}
	if prior != "" {
		// A failure to re-apply is a real error: a silently dropped selection
		// is precisely the defect this preserve-around exists to close.
		if err := config.WriteSelectedAccountID(cfgPath, prior); err != nil {
			return fmt.Errorf("knowledge setup: preserve account selection in %s: %w", cfgPath, err)
		}
	}
	fmt.Fprintf(os.Stdout, "knowledge setup: wrote %s\n", cfgPath)
	return nil
}

// coalesceCred returns stored when non-empty, else the env var value.
// The "existing config wins, env is the fallback" merge — a differing
// env value never overwrites a stored secret.
func coalesceCred(stored, env string) string {
	if stored != "" {
		return stored
	}
	return os.Getenv(env)
}

// hasCustomSections reports whether cfg carries hand-edited content
// beyond [default]/[credentials] — the sections config.RenderStarter
// does NOT regenerate and would silently drop on a --reconfigure
// rewrite. A thin predicate over the *Config config.Load already
// returned; no second parse.
func hasCustomSections(cfg *config.Config) bool {
	return cfg.Summarizer != nil || cfg.Supervisor != nil || cfg.Topics != nil || cfg.HealthProbeInterval != 0
}

// installSetupAssets runs the Claude and Codex asset installers when
// their CLI is on PATH and not gated off. It NEVER forwards setup's raw
// args — the installers' FlagSets only understand their own flags — so
// it builds a fresh slice carrying only the one overlapping flag,
// --no-mcp. A missing CLI is a silent skip, never an error.
func installSetupAssets(f *setupFlags) error {
	var assetArgs []string
	if f.noMCP {
		assetArgs = []string{"--no-mcp"}
	}
	if !f.noClaude {
		if _, err := exec.LookPath("claude"); err == nil {
			if err := claudeAssetsFn(assetArgs); err != nil {
				return fmt.Errorf("knowledge setup: install claude assets: %w", err)
			}
		}
	}
	if !f.noCodex {
		if _, err := exec.LookPath("codex"); err == nil {
			if err := codexAssetsFn(assetArgs); err != nil {
				return fmt.Errorf("knowledge setup: install codex assets: %w", err)
			}
		}
	}
	return nil
}
