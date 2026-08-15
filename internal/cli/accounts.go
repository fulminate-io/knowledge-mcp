// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
)

// accountEntry mirrors one membership in the GET /v1/me/accounts response,
// field for field against the frozen gateway contract.
type accountEntry struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Slug                  string `json:"slug"`
	Role                  string `json:"role"`
	HasActiveSubscription bool   `json:"has_active_subscription"`
}

// listAccountsResponse is the whole response body. Zero memberships arrive as
// an empty array, never null.
type listAccountsResponse struct {
	Accounts []accountEntry `json:"accounts"`
	Count    int            `json:"count"`
}

// accountsUsage is printed by `knowledge accounts --help`.
const accountsUsage = `knowledge accounts — list the Fulminate accounts you belong to

Usage:
  knowledge accounts

Lists every account your login is a member of, with the role you hold and
whether the account has an active subscription. Only subscribed accounts have
cloud graph access; the others are marked UNAVAILABLE.

Pick one with:
  knowledge account use <id|slug>
`

// fetchAccounts calls GET /v1/me/accounts and decodes the membership list.
//
// It is the ONE place that knows the endpoint's shape: `knowledge accounts`,
// `knowledge account use` and the login-time selection both consume it.
func fetchAccounts(ctx context.Context) ([]accountEntry, error) {
	tr, err := buildSyncTransportFn()
	if err != nil {
		return nil, fmt.Errorf("listing accounts requires login — run `knowledge login`: %w", err)
	}
	raw, err := tr.ListAccounts(ctx)
	if err != nil {
		var se *auth.SyncHTTPError
		if errors.As(err, &se) && se.StatusCode == 401 {
			return nil, fmt.Errorf("not logged in, or the session expired — run `knowledge login`: %w", err)
		}
		return nil, err
	}
	var parsed listAccountsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("could not read the accounts response: %w", err)
	}
	return parsed.Accounts, nil
}

// AccountsCmd implements `knowledge accounts`.
func AccountsCmd(args []string) error {
	fs := flag.NewFlagSet("accounts", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { fmt.Fprint(os.Stdout, accountsUsage) }

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	accounts, err := fetchAccounts(context.Background())
	if err != nil {
		return err
	}
	printAccounts(os.Stdout, accounts)
	return nil
}

// printAccounts renders one line per account. The usability marking is words,
// not symbols: the REASON an account cannot be selected is what the user needs
// in order to act on it.
//
// Zero memberships prints an explicit line — a user with no accounts must be
// told so, not shown a blank screen. It is not an error.
func printAccounts(out io.Writer, accounts []accountEntry) {
	if len(accounts) == 0 {
		fmt.Fprintln(out, "No accounts — your login is not a member of any Fulminate account.")
		return
	}
	for _, a := range accounts {
		usability := "cloud graph: available"
		if !a.HasActiveSubscription {
			usability = "cloud graph: UNAVAILABLE (no active subscription)"
		}
		fmt.Fprintf(out, "%s  %s  role=%s  id=%s  %s\n", a.Slug, a.Name, a.Role, a.ID, usability)
	}
}
