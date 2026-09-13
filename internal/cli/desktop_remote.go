// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

var remoteID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,199}$`)
var nativeManagementTransport = func(store auth.Store) *auth.Transport {
	return auth.NewSyncTransport(CloudEndpoint, auth.NewOAuthTokenSource(store, CloudEndpoint, AllowedAuthHosts()))
}

type desktopEnvironment struct {
	ID         string  `json:"env_id"`
	Name       string  `json:"name"`
	State      string  `json:"state"`
	Power      string  `json:"power_state"`
	Created    string  `json:"created_at,omitempty"`
	Updated    string  `json:"updated_at,omitempty"`
	Agent      string  `json:"agent,omitempty"`
	AgentState string  `json:"agent_state,omitempty"`
	Task       string  `json:"current_task,omitempty"`
	Health     string  `json:"health,omitempty"`
	VCPU       float64 `json:"vcpu,omitempty"`
	RAM        float64 `json:"ram_gb,omitempty"`
	Arch       string  `json:"arch,omitempty"`
}
type desktopRemoteResult struct {
	Account      string                `json:"account,omitempty"`
	Environments *[]desktopEnvironment `json:"environments,omitempty"`
	Code         string                `json:"code,omitempty"`
}

// environmentWire reads only the public fleet subset of the existing API.
// Custom environment values, process commands and provider credentials are never decoded.
type environmentWire struct {
	ID      string `json:"env_id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Power   string `json:"power_state"`
	Created string `json:"created_at"`
	Updated string `json:"state_changed_at"`
	Machine struct {
		VCPU float64 `json:"vcpu"`
		RAM  float64 `json:"ram_gb"`
		Arch string  `json:"arch"`
	} `json:"machine"`
	Harness *struct {
		Name  string `json:"harness_name"`
		State string `json:"state"`
		Task  string `json:"current_task"`
	} `json:"harness"`
	Health *struct {
		Status string `json:"status"`
	} `json:"health"`
}

// DesktopRemoteCmd is a fixed native-management interface, not an HTTP proxy.
func DesktopRemoteCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("remote operation required")
	}
	operation := args[0]
	if operation == "dashboard" {
		return desktopDashboardCmd(args[1:])
	}
	if operation != "list" && operation != "get" && operation != "start" && operation != "stop" {
		return errors.New("invalid remote operation")
	}
	flags := flag.NewFlagSet("desktop-remote", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	account := flags.String("account", "", "Account captured by the Desktop request")
	id := flags.String("environment", "", "Environment ID")
	if err := flags.Parse(args[1:]); err != nil {
		return errors.New("invalid remote arguments")
	}
	if flags.NArg() != 0 || !remoteID.MatchString(*account) || (operation == "list" && *id != "") || (operation != "list" && !remoteID.MatchString(*id)) {
		return errors.New("invalid remote arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	result := performDesktopRemote(ctx, *account, operation, *id)
	return json.NewEncoder(os.Stdout).Encode(result)
}
func performDesktopRemote(ctx context.Context, account, operation, id string) desktopRemoteResult {
	// Account is captured at UI intent; never re-read mutable selection here.
	if !remoteID.MatchString(account) {
		return desktopRemoteResult{Code: "account_unavailable"}
	}
	store, err := openStore()
	if err != nil {
		return desktopRemoteResult{Code: "sign_in_required"}
	}
	refresh, err := store.Get(ctx, auth.KeyRefreshToken)
	if err != nil || refresh == "" {
		return desktopRemoteResult{Code: "sign_in_required"}
	}
	transport := nativeManagementTransport(store)
	raw, err := transport.Environment(ctx, account, operation, id)
	if err != nil {
		return desktopRemoteResult{Account: account, Code: remoteErrorCode(err, operation)}
	}
	// Start/Stop acknowledge an action; read its resulting server state instead
	// of inventing a running/stopped state before the service reports it.
	if operation == "start" || operation == "stop" {
		raw, err = transport.Environment(ctx, account, "get", id)
		if err != nil {
			return desktopRemoteResult{Account: account, Code: remoteErrorCode(err, "get")}
		}
	}
	var wire []environmentWire
	if operation == "list" {
		err = json.Unmarshal(raw, &wire)
		if wire == nil && err == nil {
			err = errors.New("missing environment list")
		}
	} else {
		var env environmentWire
		err = json.Unmarshal(raw, &env)
		wire = []environmentWire{env}
	}
	if err != nil || len(wire) > 10000 {
		return desktopRemoteResult{Account: account, Code: "invalid_response"}
	}
	result := make([]desktopEnvironment, 0, len(wire))
	for _, env := range wire {
		out := desktopEnvironment{ID: env.ID, Name: env.Name, State: env.State, Power: env.Power, Created: env.Created, Updated: env.Updated, VCPU: env.Machine.VCPU, RAM: env.Machine.RAM, Arch: env.Machine.Arch}
		if env.Harness != nil {
			out.Agent = env.Harness.Name
			out.AgentState = env.Harness.State
			out.Task = env.Harness.Task
		}
		if env.Health != nil {
			out.Health = env.Health.Status
		}
		if !remoteID.MatchString(out.ID) || out.Name == "" || out.State == "" || out.Power == "" || out.VCPU < 0 || out.RAM < 0 {
			return desktopRemoteResult{Account: account, Code: "invalid_response"}
		}
		for _, value := range []string{out.Name, out.State, out.Power, out.Created, out.Updated, out.Agent, out.AgentState, out.Task, out.Health, out.Arch} {
			if len(value) > 4096 {
				return desktopRemoteResult{Account: account, Code: "invalid_response"}
			}
		}
		result = append(result, out)
	}
	return desktopRemoteResult{Account: account, Environments: &result}
}
func remoteErrorCode(err error, operation string) string {
	if status, ok := errors.AsType[*auth.ManagementHTTPError](err); ok {
		switch status.Status {
		case http.StatusUnauthorized:
			return "sign_in_required"
		case http.StatusForbidden:
			return "permission_denied"
		case http.StatusNotFound:
			return "not_found"
		case http.StatusTooManyRequests:
			return "rate_limited"
		}
	}
	if errors.Is(err, auth.ErrInvalidGrant) || errors.Is(err, auth.ErrNotFound) {
		return "sign_in_required"
	}
	if operation == "start" || operation == "stop" {
		return "action_failed"
	}
	return "unavailable"
}
