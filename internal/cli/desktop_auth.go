// SPDX-License-Identifier: Apache-2.0
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// desktopAuthResult contains only public session and membership facts. Errors
// from credential stores and OAuth responses never cross this boundary.
type desktopAuthResult struct {
	State       string          `json:"state"`
	Expiry      string          `json:"expiry,omitempty"`
	Account     string          `json:"account,omitempty"`
	Accounts    *[]accountEntry `json:"accounts,omitempty"`
	Code        string          `json:"code,omitempty"`
	AccountCode string          `json:"accountCode,omitempty"`
}

var desktopBrowserFlow = auth.RunBrowserPKCEFlowQuiet

var authConfigPath = config.DefaultPath

type desktopAuth struct {
	store      auth.Store
	configPath string
}

// DesktopAuthCmd is the fixed, machine-readable interface used by Desktop.
// Its configuration path is supplied by the main process, never the renderer.
func DesktopAuthCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("desktop-auth requires an action")
	}
	operation := args[0]
	switch operation {
	case "status", "login", "logout", "accounts", "select":
	default:
		return errors.New("unsupported authentication action")
	}
	flags := flag.NewFlagSet("desktop-auth", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config-file", "", "Knowledge configuration path")
	account := flags.String("account", "", "Account ID")
	if err := flags.Parse(args[1:]); err != nil {
		return errors.New("invalid authentication arguments")
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*path) || (operation == "select") != (*account != "") {
		return errors.New("invalid authentication arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	store, err := openStore()
	if err != nil {
		return json.NewEncoder(os.Stdout).Encode(desktopAuthResult{State: "unavailable", Code: "credential_unavailable"})
	}
	d := desktopAuth{store: store, configPath: *path}
	return json.NewEncoder(os.Stdout).Encode(d.perform(ctx, operation, *account))
}
func (d desktopAuth) status(ctx context.Context) desktopAuthResult {
	result := desktopAuthResult{State: "signed_out"}
	values := make(map[string]string, 4)
	for _, key := range []string{auth.KeyAccessToken, auth.KeyAccessTokenExpiry, auth.KeyRefreshToken, auth.KeyClientID} {
		value, err := d.store.Get(ctx, key)
		if err != nil && !errors.Is(err, auth.ErrNotFound) {
			result.State = "unavailable"
			result.Code = "credential_unavailable"
			return result
		}
		values[key] = value
	}
	switch {
	case values[auth.KeyAccessToken] != "":
		expiry, err := time.Parse(time.RFC3339, values[auth.KeyAccessTokenExpiry])
		if err != nil || !time.Now().Before(expiry) {
			result.State = "expired"
		} else {
			result.State = "signed_in"
			result.Expiry = expiry.Format(time.RFC3339)
		}
	case values[auth.KeyRefreshToken] != "":
		result.State = "unpublished"
	}
	selected, err := config.ReadSelectedAccountID(d.configPath)
	if err != nil {
		result.AccountCode = "account_unavailable"
	} else {
		result.Account = selected
	}
	return result
}
func (d desktopAuth) perform(ctx context.Context, operation, account string) desktopAuthResult {
	var code string
	switch operation {
	case "login":
		code = d.login(ctx)
	case "logout":
		code = d.logout(ctx)
	case "select":
		code = d.selectAccount(ctx, account)
	}
	result := d.status(ctx)
	if code != "" {
		result.Code = code
	}
	if operation == "accounts" || operation == "select" || operation == "login" {
		accounts, err := fetchAccounts(ctx)
		if err != nil {
			result.AccountCode = "account_unavailable"
		} else {
			result.Accounts = &accounts
		}
	}
	return result
}
func (d desktopAuth) login(ctx context.Context) string {
	endpoints, err := discoverFn(ctx, CloudEndpoint, allowedAuthHosts)
	if err != nil {
		return "authentication_failed"
	}
	clientID, tr, err := desktopBrowserFlow(ctx, endpoints)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "cancelled"
		}
		return "authentication_failed"
	}
	if clientID == "" || tr.RefreshToken == "" || tr.AccessToken == "" {
		return "session_incomplete"
	}
	if err = d.store.Set(ctx, auth.KeyClientID, clientID); err != nil {
		return "session_incomplete"
	}
	if err = d.store.Set(ctx, auth.KeyRefreshToken, tr.RefreshToken); err != nil {
		return "session_incomplete"
	}
	if err = auth.PublishSessionToken(ctx, d.store, tr); err != nil {
		return "session_incomplete"
	}
	accounts, err := fetchAccounts(ctx)
	if err != nil {
		return "account_unavailable"
	}
	selected, err := config.ReadSelectedAccountID(d.configPath)
	if err != nil {
		return "account_unavailable"
	}
	if selected != "" {
		a, ok := matchAccount(accounts, selected)
		if !ok || !a.HasActiveSubscription {
			return "account_ineligible"
		}
		return ""
	}
	if len(accounts) == 0 {
		return "account_ineligible"
	}
	if err = config.WriteSelectedAccountID(d.configPath, accounts[0].ID); err != nil {
		return "account_write_failed"
	}
	if !accounts[0].HasActiveSubscription {
		return "account_ineligible"
	}
	return ""
}
func (d desktopAuth) selectAccount(ctx context.Context, id string) string {
	accounts, err := fetchAccounts(ctx)
	if err != nil {
		return "account_unavailable"
	}
	matched, ok := matchAccount(accounts, id)
	if !ok || matched.ID != id || !matched.HasActiveSubscription {
		return "account_ineligible"
	}
	if err = config.WriteSelectedAccountID(d.configPath, matched.ID); err != nil {
		return "account_write_failed"
	}
	return ""
}
func (d desktopAuth) logout(ctx context.Context) string {
	code := ""
	token, err := d.store.Get(ctx, auth.KeyRefreshToken)
	if err == nil && token != "" {
		endpoints, discErr := discoverFn(ctx, CloudEndpoint, allowedAuthHosts)
		if discErr != nil {
			code = "revocation_unavailable"
		} else if err = auth.RevokeRefreshTokenResult(ctx, endpoints.RevocationEndpoint, token); err != nil {
			code = "revocation_unavailable"
		}
	} else if err != nil && !errors.Is(err, auth.ErrNotFound) {
		code = "revocation_unavailable"
	}
	if err = deleteCredentials(ctx, d.store); err != nil {
		return "cleanup_failed"
	}
	return code
}
func deleteCredentials(ctx context.Context, store auth.Store) error {
	var failures []error
	for _, key := range []string{auth.KeyRefreshToken, auth.KeyClientID, auth.KeyAccessToken, auth.KeyAccessTokenExpiry} {
		if err := store.Delete(ctx, key); err != nil && !errors.Is(err, auth.ErrNotFound) {
			failures = append(failures, errors.New("credential cleanup failed"))
		}
	}
	return errors.Join(failures...)
}
