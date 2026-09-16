// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

type environmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type environmentKnowledgeSection struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}
type environmentKnowledge struct {
	Enabled    *bool                        `json:"enabled,omitempty"`
	Default    *environmentKnowledgeSection `json:"default,omitempty"`
	Summarizer *environmentKnowledgeSection `json:"summarizer,omitempty"`
	Dream      *environmentKnowledgeSection `json:"dream,omitempty"`
	Supervisor *environmentKnowledgeSection `json:"supervisor,omitempty"`
	Topics     *environmentKnowledgeSection `json:"topics,omitempty"`
}

// Configuration is returned only for explicit detail and mutation requests.
// Provider credential values and process telemetry are never part of this DTO.
type environmentConfiguration struct {
	EnabledProviderIDs []string              `json:"enabled_provider_ids"`
	CustomEnvVars      []environmentVariable `json:"custom_env_vars"`
	KnowledgeConfig    *environmentKnowledge `json:"knowledge_config"`
	HostedPort         int                   `json:"hosted_port"`
	HealthcheckPath    string                `json:"healthcheck_path"`
}
type environmentCreate struct {
	environmentConfiguration
	Name            string   `json:"name"`
	BaseTemplate    string   `json:"base_template"`
	CustomImage     string   `json:"custom_image"`
	SpecID          string   `json:"spec_id"`
	VolumeName      string   `json:"volume_name"`
	WorkspaceDiskGB int      `json:"workspace_disk_gb"`
	RepoURL         string   `json:"repo_url"`
	SetupCommands   []string `json:"setup_commands"`
}
type environmentDelete struct {
	KeepVolume bool   `json:"keep_volume"`
	VolumeName string `json:"volume_name"`
}
type environmentRequest struct {
	Operation   string          `json:"operation"`
	Account     string          `json:"account"`
	Environment string          `json:"environment,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

func decodeManagementInput(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > 128<<10 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("invalid management request")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return errors.New("invalid management request")
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("invalid management request")
	}
	return nil
}
func desktopEnvironmentCmd(args []string) error {
	// Bad configuration, refused before any work — see DesktopAuthCmd for why
	// this is a hard error rather than a JSON result code.
	if _, err := auth.CredentialNamespace(); err != nil {
		return err
	}
	var raw []byte
	var err error
	switch len(args) {
	case 0:
		raw, err = io.ReadAll(io.LimitReader(stdinReader, (128<<10)+1))
	case 1:
		raw = []byte(args[0])
	default:
		return errors.New("invalid environment request")
	}
	var request environmentRequest
	if err != nil || decodeManagementInput(raw, &request) != nil || !validEnvironmentRequest(request) {
		return errors.New("invalid environment request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return json.NewEncoder(os.Stdout).Encode(performEnvironmentWrite(ctx, request))
}
func validEnvironmentRequest(r environmentRequest) bool {
	if !remoteID.MatchString(r.Account) {
		return false
	}
	if r.Operation == "create" {
		if r.Environment != "" {
			return false
		}
	} else if !remoteID.MatchString(r.Environment) {
		return false
	}
	switch r.Operation {
	case "create":
		var in environmentCreate
		if decodeManagementInput(r.Payload, &in) != nil || strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || (in.BaseTemplate == "") == (in.CustomImage == "") {
			return false
		}
		if in.WorkspaceDiskGB != 0 && (in.WorkspaceDiskGB < 50 || in.WorkspaceDiskGB > 500 || in.VolumeName != "") {
			return false
		}
		return validEnvironmentConfiguration(in.environmentConfiguration)
	case "update":
		var in environmentConfiguration
		return decodeManagementInput(r.Payload, &in) == nil && validEnvironmentConfiguration(in)
	case "delete":
		if len(r.Payload) == 0 {
			return true
		}
		var in environmentDelete
		return decodeManagementInput(r.Payload, &in) == nil && ((in.KeepVolume && strings.TrimSpace(in.VolumeName) != "") || (!in.KeepVolume && in.VolumeName == ""))
	}
	return false
}
func validEnvironmentConfiguration(in environmentConfiguration) bool {
	if in.HostedPort < 0 || in.HostedPort > 65535 || in.HostedPort == 22 || in.HostedPort == 15023 {
		return false
	}
	if in.HealthcheckPath != "" && (in.HostedPort == 0 || !strings.HasPrefix(in.HealthcheckPath, "/") || strings.ContainsAny(in.HealthcheckPath, "\r\n")) {
		return false
	}
	for _, v := range in.CustomEnvVars {
		if strings.TrimSpace(v.Name) == "" {
			return false
		}
	}
	return true
}
func performEnvironmentWrite(ctx context.Context, r environmentRequest) desktopRemoteResult {
	if !validEnvironmentRequest(r) {
		return desktopRemoteResult{Code: "invalid_request"}
	}
	store, err := openStore()
	if err != nil {
		return desktopRemoteResult{Code: "sign_in_required"}
	}
	refresh, err := store.Get(ctx, auth.KeyRefreshToken)
	if err != nil || refresh == "" {
		return desktopRemoteResult{Code: "sign_in_required"}
	}
	raw, err := nativeManagementTransport(store).EnvironmentWrite(ctx, r.Account, r.Operation, r.Environment, r.Payload)
	if err != nil {
		return desktopRemoteResult{Account: r.Account, Code: remoteErrorCode(err, r.Operation)}
	}
	if r.Operation == "delete" {
		return desktopRemoteResult{Account: r.Account, Accepted: true}
	}
	// Provision can succeed while the server's subsequent read fails. Its receipt
	// still carries the created ID, but no authoritative power/configuration.
	// Preserve that identity for a later explicit GET; never replay creation.
	if r.Operation == "create" {
		var receipt struct {
			ID    string `json:"env_id"`
			Power string `json:"power_state"`
		}
		if json.Unmarshal(raw, &receipt) == nil && remoteID.MatchString(receipt.ID) && receipt.Power == "" {
			return desktopRemoteResult{Account: r.Account, Accepted: true, Environment: receipt.ID, ReadbackPending: true}
		}
	}
	return projectDesktopEnvironments(r.Account, r.Operation, r.Environment, raw)
}
