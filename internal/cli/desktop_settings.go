// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

type desktopSettingsSection struct {
	Saved     map[string]string `json:"saved"`
	Effective map[string]string `json:"effective"`
}
type desktopSettingsResult struct {
	*config.SettingsDocument
	State       string                            `json:"state"`
	Code        string                            `json:"code,omitempty"`
	Saved       bool                              `json:"saved"`
	Sections    map[string]desktopSettingsSection `json:"sections,omitempty"`
	Credentials map[string]string                 `json:"credentials,omitempty"`
	Validation  []string                          `json:"validation,omitempty"`
}

var settingsSections = []string{"default", "summarizer", "supervisor", "topics"}
var settingsFields = []string{"provider", "model", "cli_bin", "base_url"}
var settingsCredentials = []string{"voyage_api_key", "linear_api_key", "anthropic_api_key", "openai_api_key", "gemini_api_key", "cohere_api_key"}

// DesktopSettingsCmd reads or updates main-selected configuration. Secret input
// travels over stdin, never command arguments. Public errors contain no input.
func DesktopSettingsCmd(args []string) error {
	if len(args) < 1 || (args[0] != "read" && args[0] != "save") {
		return errors.New("unsupported settings action")
	}
	flags := flag.NewFlagSet("desktop-settings", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config-file", "", "Knowledge configuration path")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*path) {
		return errors.New("invalid settings arguments")
	}
	return json.NewEncoder(os.Stdout).Encode(runDesktopSettings(*path, args[0], os.Stdin))
}
func settingsFailure(code string) desktopSettingsResult {
	return desktopSettingsResult{State: "error", Code: code}
}
func runDesktopSettings(path, operation string, input io.Reader) desktopSettingsResult {
	data, err := os.ReadFile(path)
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return settingsFailure("config_unreadable")
	}
	c, err := config.Parse(data)
	if err != nil {
		return settingsFailure("config_invalid")
	}
	if operation == "read" {
		r := settingsView(c)
		r.SettingsDocument, err = config.ReadSettingsDocument(data)
		if err != nil {
			return settingsFailure("config_invalid")
		}
		if missing {
			r.State = "missing"
		}
		return r
	}
	if operation != "save" {
		return settingsFailure("invalid_request")
	}
	var request struct {
		Edits   map[string]map[string]string `json:"edits"`
		Patches []config.SettingsPatch       `json:"patches"`
	}
	raw, readErr := io.ReadAll(io.LimitReader(input, 65537))
	if readErr != nil || len(raw) > 65536 {
		return settingsFailure("invalid_request")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil || dec.Decode(new(any)) != io.EOF || (len(request.Patches) == 0 && !validSettingsEdits(request.Edits)) || (len(request.Patches) > 0 && len(request.Edits) > 0) {
		return settingsFailure("invalid_request")
	}
	var edited []byte
	if len(request.Patches) > 0 {
		edited, err = config.EditSettingsPatches(data, request.Patches)
	} else {
		edited, err = config.EditSettings(data, request.Edits)
	}
	if err != nil {
		return settingsFailure("invalid_settings")
	}
	candidate, err := config.Parse(edited)
	if err != nil {
		return settingsFailure("invalid_settings")
	}
	view := settingsView(candidate)
	view.SettingsDocument, err = config.ReadSettingsDocument(edited)
	if err != nil {
		return settingsFailure("invalid_settings")
	}
	if settingsGated(request.Edits, request.Patches) && len(view.Validation) > 0 {
		prior := settingsView(c)
		if settingsBreaksResolvedConsumer(prior.Validation, view.Validation) {
			prior.State = "error"
			prior.Code = "invalid_settings"
			prior.Validation = view.Validation
			return prior
		}
	}
	if err = os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return settingsFailure("config_write_failed")
	}
	if err = config.WriteFileAtomic(path, edited, 0600); err != nil {
		return settingsFailure("config_write_failed")
	}
	view.Saved = true
	return view
}

// settingsGated reports whether a save is subject to the consumer rule at all.
// ONE rule covers both request bodies: the legacy section edits and the typed
// patches are gated identically, so the same change cannot be refused through
// one body and saved through the other. A request that touches nothing but the
// credentials table is exempt, because clearing a key is a deliberate removal
// whose consequence the form already reports.
func settingsGated(edits map[string]map[string]string, patches []config.SettingsPatch) bool {
	for section := range edits {
		if section != "credentials" {
			return true
		}
	}
	for _, patch := range patches {
		if len(patch.Path) == 0 || patch.Path[0] != "credentials" {
			return true
		}
	}
	return false
}

// settingsBreaksResolvedConsumer reports whether the candidate cannot resolve a
// consumer that the PRIOR document could. That comparison, rather than a test
// of the candidate alone, is what the gate refuses on.
//
// The prior state is load-bearing because a first-run configuration resolves NO
// consumer: with no default provider and model, summarizer, supervisor and
// topics all fail before the user has typed anything. A candidate-only gate
// therefore refuses a fresh user's first auto_update toggle, their first
// embedder table, and every one-table-at-a-time step of setting the thing up,
// naming consumers they have not reached yet. Comparing against the prior
// document lets a save that leaves an already-broken consumer broken through,
// while a change that breaks a consumer which did resolve is still refused.
//
// Both sets come from settingsView, which appends in settingsSections order, so
// this is a membership test over at most three names and needs no map.
func settingsBreaksResolvedConsumer(prior, candidate []string) bool {
	for _, name := range candidate {
		if !slices.Contains(prior, name) {
			return true
		}
	}
	return false
}
func validSettingsEdits(edits map[string]map[string]string) bool {
	if len(edits) == 0 {
		return false
	}
	for section, fields := range edits {
		allowed := settingsFields
		if section == "credentials" {
			allowed = settingsCredentials
		} else if !settingsContains(settingsSections, section) {
			return false
		}
		if len(fields) == 0 {
			return false
		}
		for key, value := range fields {
			if !settingsContains(allowed, key) || len(value) > 16384 {
				return false
			}
			if key == "provider" && value != "" && !config.Provider(value).IsValid() {
				return false
			}
		}
	}
	return true
}
func settingsContains(values []string, value string) bool {
	return slices.Contains(values, value)
}
func settingsSection(s config.Section) map[string]string {
	return map[string]string{"provider": string(s.Provider), "model": s.Model, "cli_bin": s.CLIBin, "base_url": s.BaseURL}
}
func settingsView(c *config.Config) desktopSettingsResult {
	r := desktopSettingsResult{State: "configured", Sections: map[string]desktopSettingsSection{}, Credentials: map[string]string{}}
	sections := []*config.Section{&c.Default, c.Summarizer, c.Supervisor, c.Topics}
	for i, name := range settingsSections {
		s := config.Section{}
		if sections[i] != nil {
			s = *sections[i]
		}
		effective := s
		if i > 0 {
			resolved, err := c.Resolve(config.Consumer(name))
			if err == nil {
				effective = resolved
			}
			if c.Validate([]config.Consumer{config.Consumer(name)}) != nil {
				r.Validation = append(r.Validation, name)
			}
		}
		r.Sections[name] = desktopSettingsSection{Saved: settingsSection(s), Effective: settingsSection(effective)}
	}
	cr := c.Credentials
	if cr == nil {
		cr = &config.Credentials{}
	}
	values := []string{cr.VoyageAPIKey, cr.LinearAPIKey, cr.AnthropicAPIKey, cr.OpenAIAPIKey, cr.GeminiAPIKey, cr.CohereAPIKey}
	for i, key := range settingsCredentials {
		source := "missing"
		if values[i] != "" {
			source = "saved"
		} else if os.Getenv(strings.ToUpper(key)) != "" {
			source = "environment"
		}
		r.Credentials[key] = source
	}
	return r
}
