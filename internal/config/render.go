// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"sync"
	"text/template"
)

// starterTmpl is the embedded text/template body for the auto-generated
// starter config. text/template (NOT html/template) — the output is TOML,
// not HTML, and html/template's escaping would mangle quotes and angle
// brackets.
//
//go:embed starter.tmpl
var starterTmpl string

// parsedStarter caches the parsed template after first use. The template
// body is constant, so re-parsing on every Render is wasted work; the
// sync.Once gate keeps the parse cost out of the hot path.
var (
	parsedStarter     *template.Template
	parsedStarterOnce sync.Once
	parsedStarterErr  error
)

// Model is a provider-specific model identifier. It mirrors
// domains/llm.Model in shape but stays local so domains/config remains a
// leaf package with no upstream deps.
type Model string

// String returns m as a plain string.
func (m Model) String() string { return string(m) }

// DetectedProvider is the result of an auto-detect walk: which provider
// won, which default model to seed [default].model with, and (for CLI
// providers) the absolute path to the CLI binary. CLIBin is populated
// from the exec.LookPath result that determined CLI-provider
// availability — the starter file then carries an explicit cli_bin so
// the validator's "must be set for CLI providers" rule passes on
// first run without manual editing.
type DetectedProvider struct {
	Provider Provider
	Model    Model
	CLIBin   string
}

// Render returns the TOML body of a starter config seeded with detected.
//
// The template lives in starter.tmpl and is parsed once via sync.Once.
// Per-consumer example blocks in the starter are LITERAL TOML comments
// (not template logic), so the rendered output differs across detected
// providers only in the [default] block.
func Render(detected DetectedProvider) (string, error) {
	tmpl, err := loadStarterTemplate()
	if err != nil {
		return "", err
	}
	if !detected.Provider.IsValid() {
		return "", fmt.Errorf("config.Render: invalid provider %q", detected.Provider)
	}
	if detected.Model == "" {
		return "", fmt.Errorf("config.Render: empty model for provider %q", detected.Provider)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, detected); err != nil {
		return "", fmt.Errorf("config.Render: execute: %w", err)
	}
	return buf.String(), nil
}

// loadStarterTemplate parses starterTmpl exactly once and memoizes the
// result. Subsequent calls return the cached *template.Template (or the
// cached parse error).
func loadStarterTemplate() (*template.Template, error) {
	parsedStarterOnce.Do(func() {
		t, err := template.New("starter").Parse(starterTmpl)
		if err != nil {
			parsedStarterErr = fmt.Errorf("config: parse starter.tmpl: %w", err)
			return
		}
		parsedStarter = t
	})
	return parsedStarter, parsedStarterErr
}
