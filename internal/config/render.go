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
// internal/llm.Model in shape but stays local so internal/config remains a
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

// starterView is the template data model: the detected provider fields
// (promoted via embedding so the existing {{.Provider}}/{{.Model}}/
// {{.CLIBin}} references keep working) plus a Credentials value. When
// any Credentials field is non-empty the template emits a REAL,
// uncommented [credentials] table with ONLY the set keys; when all are
// empty it emits the commented guidance block (byte-identical to the
// pre-credentials starter).
type starterView struct {
	DetectedProvider
	Credentials Credentials
}

// Render returns the TOML body of a starter config seeded with detected
// and NO credentials — the commented [credentials] guidance block. It
// is the credential-free shim over RenderStarter; existing callers
// (ensureFileExists, the golden tests) get byte-identical output.
func Render(detected DetectedProvider) (string, error) {
	return RenderStarter(detected, Credentials{})
}

// RenderStarter returns the TOML body of a starter config seeded with
// detected and any supplied credentials. Credential values are written
// into a real [credentials] table (only the set keys appear); an empty
// Credentials leaves the commented guidance block instead. The output
// round-trips through config.Parse.
//
// The template lives in starter.tmpl and is parsed once via sync.Once.
// Per-consumer example blocks in the starter are LITERAL TOML comments
// (not template logic), so the rendered output differs across detected
// providers only in the [default] block and the [credentials] tail.
func RenderStarter(detected DetectedProvider, creds Credentials) (string, error) {
	tmpl, err := loadStarterTemplate()
	if err != nil {
		return "", err
	}
	// An EMPTY provider is the valid "unconfigured" render (no provider
	// detected — BM25-only degrade): the template emits a commented
	// [default] guidance block instead of an active provider/model. Only
	// validate when a provider IS supplied.
	if detected.Provider != "" {
		if !detected.Provider.IsValid() {
			return "", fmt.Errorf("config.RenderStarter: invalid provider %q", detected.Provider)
		}
		if detected.Model == "" {
			return "", fmt.Errorf("config.RenderStarter: empty model for provider %q", detected.Provider)
		}
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, starterView{DetectedProvider: detected, Credentials: creds}); err != nil {
		return "", fmt.Errorf("config.RenderStarter: execute: %w", err)
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
