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
	transport  *auth.Transport // Optional fixture transport; production uses this store/config.
}

// DesktopAuthCmd is the fixed, machine-readable interface used by Desktop.
// Its configuration path is supplied by the main process, never the renderer.
func DesktopAuthCmd(args []string) error {
	// A malformed credential-namespace selector is bad configuration, not a
	// signed-out machine. It is refused HERE, before any work and before the
	// store is opened, so the caller gets a hard error on stderr instead of a
	// JSON result code it would read as "ask the user to sign in". The same
	// predicate backs auth.OpenStore, so this is an ordering guarantee rather
	// than a second gate.
	if _, err := auth.CredentialNamespace(); err != nil {
		return err
	}
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
	return d.sessionStatus(ctx, false)
}

// sessionStatus dates a credential only after this operation authenticated it.
func (d desktopAuth) sessionStatus(ctx context.Context, validated bool) desktopAuthResult {
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
			if validated {
				result.Expiry = expiry.Format(time.RFC3339)
			}
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

// perform owns a single operation's authenticated result. Stored-only status
// never carries expiry: reading a timestamp does not validate a credential.
func (d desktopAuth) perform(ctx context.Context, operation, account string) desktopAuthResult {
	var accounts []accountEntry
	var code string
	var accountErr error
	switch operation {
	case "login":
		accounts, code, accountErr = d.login(ctx)
	case "logout":
		code = d.logout(ctx)
	case "select":
		accounts, code, accountErr = d.selectAccount(ctx, account)
	case "accounts":
		accounts, accountErr = d.loadAccounts(ctx)
	}
	result := d.sessionStatus(ctx, accounts != nil && accountErr == nil)
	if code != "" {
		result.Code = code
	}
	if accountErr != nil {
		result.AccountCode = "account_unavailable"
		if desktopSignInRequired(accountErr) {
			result.State = "expired"
			result.AccountCode = "sign_in_required"
		}
	} else if accounts != nil {
		result.Accounts = &accounts
	}
	return result
}

func desktopSignInRequired(err error) bool {
	if errors.Is(err, auth.ErrInvalidGrant) || errors.Is(err, auth.ErrNoSession) || errors.Is(err, auth.ErrSessionExpired) || errors.Is(err, auth.ErrNotFound) {
		return true
	}
	var oauth *auth.OAuthError
	var response *auth.SyncHTTPError
	return (errors.As(err, &oauth) && oauth.StatusCode == http.StatusUnauthorized) || (errors.As(err, &response) && response.StatusCode == http.StatusUnauthorized)
}

var desktopAccountTransport = func(store auth.Store, configPath string) *auth.Transport {
	return auth.NewSyncTransport(CloudEndpoint, auth.NewOAuthTokenSource(store, CloudEndpoint, AllowedAuthHosts()), syncTransportProof(), auth.WithAccountSelection(auth.NewAccountSelection(configPath, time.Second)))
}

func (d desktopAuth) loadAccounts(ctx context.Context) ([]accountEntry, error) {
	tr := d.transport
	if tr == nil {
		tr = desktopAccountTransport(d.store, d.configPath)
	}
	return fetchAccountsWithTransport(ctx, tr)
}

// login reports non-membership failures through sanitized public codes.
// The error return is reserved for membership authentication classification.
//
//nolint:nilerr // Public outcome code carries the handled store/browser/config failure.
func (d desktopAuth) login(ctx context.Context) ([]accountEntry, string, error) {
	endpoints, err := discoverFn(ctx, CloudEndpoint, allowedAuthHosts)
	if err != nil {
		return nil, "authentication_failed", nil
	}
	clientID, tr, err := desktopBrowserFlow(ctx, endpoints)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, "cancelled", nil
		}
		return nil, "authentication_failed", nil
	}
	if clientID == "" || tr.RefreshToken == "" || tr.AccessToken == "" {
		return nil, "session_incomplete", nil
	}
	if err = d.store.Set(ctx, auth.KeyClientID, clientID); err != nil {
		return nil, "session_incomplete", nil
	}
	if err = d.store.Set(ctx, auth.KeyRefreshToken, tr.RefreshToken); err != nil {
		return nil, "session_incomplete", nil
	}
	if err = auth.PublishSessionToken(ctx, d.store, tr); err != nil {
		return nil, "session_incomplete", nil
	}
	accounts, err := d.loadAccounts(ctx)
	if err != nil {
		return nil, "", err
	}
	selected, err := config.ReadSelectedAccountID(d.configPath)
	if err != nil {
		return accounts, "account_unavailable", nil
	}
	if selected != "" {
		a, ok := matchAccount(accounts, selected)
		if !ok || !a.HasActiveSubscription {
			return accounts, "account_ineligible", nil
		}
		return accounts, "", nil
	}
	if len(accounts) == 0 {
		return accounts, "account_ineligible", nil
	}
	if err = config.WriteSelectedAccountID(d.configPath, accounts[0].ID); err != nil {
		return accounts, "account_write_failed", nil
	}
	if !accounts[0].HasActiveSubscription {
		return accounts, "account_ineligible", nil
	}
	return accounts, "", nil
}

//nolint:nilerr // Public account_write_failed reports the handled config error.
func (d desktopAuth) selectAccount(ctx context.Context, id string) ([]accountEntry, string, error) {
	accounts, err := d.loadAccounts(ctx)
	if err != nil {
		return nil, "", err
	}
	matched, ok := matchAccount(accounts, id)
	if !ok || matched.ID != id || !matched.HasActiveSubscription {
		return accounts, "account_ineligible", nil
	}
	if err = config.WriteSelectedAccountID(d.configPath, matched.ID); err != nil {
		return accounts, "account_write_failed", nil
	}
	return accounts, "", nil
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
