// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
)

// detectDeps is the dependency-injection seam for auto-detect. Tests
// inject deterministic lookPath / getenv functions; production code uses
// defaultDetectDeps() which wraps exec.LookPath + os.Getenv.
type detectDeps struct {
	lookPath func(string) (string, error)
	getenv   func(string) string
}

// defaultDetectDeps returns a detectDeps that talks to the real PATH and
// environment. Constructed at call time to avoid baking process state
// into a package-level value.
func defaultDetectDeps() detectDeps {
	return detectDeps{
		lookPath: exec.LookPath,
		getenv:   os.Getenv,
	}
}

// defaultModels seeds [default].model when the auto-detector picks a
// provider without an explicit user override. The summarizer is the
// only consumer that resolves through [default] today, and summarization
// is text-extraction work — haiku-class models are sufficient. Users
// who add a [dream] section should pick an opus-class model since dream
// phases run multi-step reasoning.
var defaultModels = map[Provider]Model{
	ProviderAnthropic: "claude-haiku-4-5-20251001",
	ProviderOpenAI:    "gpt-4o-mini",
	ProviderGemini:    "gemini-2.5-flash",
	ProviderClaudeCLI: "claude-haiku-4-5",
	ProviderCodexCLI:  "o4-mini-codex",
}

// localPrecedence is the auto-detect walk for loopback binds. CLI
// providers come first because they're the lowest-friction local-dev
// experience (no API key required); API providers follow.
var localPrecedence = []Provider{
	ProviderClaudeCLI,
	ProviderCodexCLI,
	ProviderAnthropic,
	ProviderOpenAI,
	ProviderGemini,
}

// serverPrecedence is the auto-detect walk for non-loopback binds. CLI
// providers are excluded because shell-out doesn't make sense in a
// server context (no interactive auth, no consistent PATH across pods).
var serverPrecedence = []Provider{
	ProviderAnthropic,
	ProviderOpenAI,
	ProviderGemini,
}

// IsLoopback reports whether addr names a loopback endpoint. The check
// covers both TCP loopbacks (IP.IsLoopback) and Unix-domain sockets
// (always treated as local). A nil addr is non-loopback by convention —
// the auto-detector picks server precedence in that case.
func IsLoopback(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	switch a := addr.(type) {
	case *net.TCPAddr:
		return a.IP != nil && a.IP.IsLoopback()
	case *net.UnixAddr:
		return true
	default:
		return false
	}
}

// detectLocal walks localPrecedence and returns the first available
// provider plus its default model. Availability for CLI providers is
// determined by exec.LookPath; for API providers, by env-var presence.
func detectLocal(deps detectDeps) (DetectedProvider, error) {
	return detectWalk(deps, localPrecedence)
}

// detectServer walks serverPrecedence and returns the first available
// provider plus its default model. Server context excludes CLI providers
// — only env-var checks apply.
func detectServer(deps detectDeps) (DetectedProvider, error) {
	return detectWalk(deps, serverPrecedence)
}

// detectWalk runs the precedence walk for a given list. Returns the
// first match along with the default model for that provider. For CLI
// providers, the CLIBin field of the returned DetectedProvider holds
// the absolute path resolved via exec.LookPath at detection time so
// the rendered starter is immediately validator-clean. If no provider
// matches, an error names every option that was checked.
func detectWalk(deps detectDeps, precedence []Provider) (DetectedProvider, error) {
	for _, p := range precedence {
		if avail, cliBin := providerAvailability(deps, p); avail {
			return DetectedProvider{Provider: p, Model: defaultModels[p], CLIBin: cliBin}, nil
		}
	}
	return DetectedProvider{}, fmt.Errorf("config: auto-detect: no provider available; checked %v", precedence)
}

// providerAvailability returns (true, "") for an available API provider,
// (true, absolutePath) for an available CLI provider, or (false, "")
// when the provider isn't usable in the current environment. The CLI
// path is captured at detection time so the starter template can write
// it into cli_bin without a second LookPath call.
func providerAvailability(deps detectDeps, p Provider) (bool, string) {
	if env, ok := providerEnvVar[p]; ok {
		return deps.getenv(env) != "", ""
	}
	if bin, ok := providerCLIBinary[p]; ok {
		path, err := deps.lookPath(bin)
		if err != nil {
			return false, ""
		}
		return true, path
	}
	return false, ""
}

// LoadOrAutoDetect returns the active config, opening or auto-creating
// the file at path.
//
// Behavior:
//
//  1. Stat path. If the file exists, Load + Validate(consumers) for the
//     two consumers wired in this ticket scope (summarizer + transformer;
//     dream is parser-only and skipped). On success the singleton is
//     populated by Load and the bool return is false.
//
//  2. If the file does not exist, pick the precedence list with
//     IsLoopback(bindAddr): loopback → localPrecedence; everything else
//     (or nil) → serverPrecedence. Walk it, render the starter via
//     Render, MkdirAll the parent dir, WriteFile the rendered body, then
//     Load + Validate as in (1). The bool return is true to let the
//     caller log "wrote starter config".
//
// Any other os.Stat error (permission, broken symlink) is returned
// verbatim — the server should fail fast rather than try to recover.
func LoadOrAutoDetect(path string, bindAddr net.Addr) (*Config, bool, error) {
	wroteStarter, err := ensureFileExists(path, bindAddr)
	if err != nil {
		return nil, false, err
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, wroteStarter, err
	}
	if err := cfg.Validate([]Consumer{ConsumerSummarizer}); err != nil {
		return nil, wroteStarter, fmt.Errorf("config.LoadOrAutoDetect: validate %s: %w", path, err)
	}
	return cfg, wroteStarter, nil
}

// ensureFileExists checks for path. If it's missing, the auto-detector
// runs against bindAddr's precedence list and the rendered starter is
// written to path (creating parent dirs). The returned bool is true iff
// a starter was written.
func ensureFileExists(path string, bindAddr net.Addr) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("config.LoadOrAutoDetect: stat %s: %w", path, err)
	}
	deps := defaultDetectDeps()
	var detected DetectedProvider
	var detectErr error
	if IsLoopback(bindAddr) {
		detected, detectErr = detectLocal(deps)
	} else {
		detected, detectErr = detectServer(deps)
	}
	if detectErr != nil {
		return false, detectErr
	}
	body, err := Render(detected)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return false, fmt.Errorf("config.LoadOrAutoDetect: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return false, fmt.Errorf("config.LoadOrAutoDetect: write %s: %w", path, err)
	}
	return true, nil
}
