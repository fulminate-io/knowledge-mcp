// SPDX-License-Identifier: Apache-2.0
package cli

import (
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
		if missing {
			r.State = "missing"
		}
		return r
	}
	if operation != "save" {
		return settingsFailure("invalid_request")
	}
	var request struct {
		Edits map[string]map[string]string `json:"edits"`
	}
	dec := json.NewDecoder(io.LimitReader(input, 65537))
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil || dec.Decode(new(any)) != io.EOF || !validSettingsEdits(request.Edits) {
		return settingsFailure("invalid_request")
	}
	edited, err := config.EditSettings(data, request.Edits)
	if err != nil {
		return settingsFailure("invalid_settings")
	}
	candidate, err := config.Parse(edited)
	if err != nil {
		return settingsFailure("invalid_settings")
	}
	view := settingsView(candidate)
	for section := range request.Edits {
		if section != "credentials" && len(view.Validation) > 0 {
			prior := settingsView(c)
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
