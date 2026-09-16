// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"os"
	"strings"
	"testing"
)

// account_daemon_entry_test.go is the SEAM test for the two daemon entry points:
// both sides are real — the real membership decoder against an httptest gateway
// serving the frozen body shape, and the real write flow against a scratch
// config — because the thing that would break silently is the daemon getting a
// SECOND membership decoder or a second write flow, and a doubled far side would
// hide exactly that.

// TestLookupAccount_UsesTheRealDecoderAndMatchesIDFirstThenSlug pins what
// account_for_session's membership check is: the one decoder, matching on id
// first and slug second.
func TestLookupAccount_UsesTheRealDecoderAndMatchesIDFirstThenSlug(t *testing.T) {
	serveAccounts(t, twoAccountsBody)

	byID, err := LookupAccount(context.Background(), "acct_01ACME")
	if err != nil {
		t.Fatalf("LookupAccount by id: %v", err)
	}
	if byID.ID != "acct_01ACME" || byID.Slug != "acme" || !byID.HasActiveSubscription {
		t.Errorf("by id = %+v, want the acme membership", byID)
	}

	bySlug, err := LookupAccount(context.Background(), "acme")
	if err != nil {
		t.Fatalf("LookupAccount by slug: %v", err)
	}
	if bySlug.ID != "acct_01ACME" {
		t.Errorf("by slug = %+v, want the acme membership", bySlug)
	}
}

// TestLookupAccount_ChecksMembershipOnlyNeverSubscription is the asymmetry the
// owner ruled on: the client checks membership, and the gateway is the authority
// on subscription. An unsubscribed MEMBER resolves here; refusing it would make
// it unbindable per-session for a reason this client is not the authority on.
func TestLookupAccount_ChecksMembershipOnlyNeverSubscription(t *testing.T) {
	serveAccounts(t, twoAccountsBody)

	got, err := LookupAccount(context.Background(), "hobby")
	if err != nil {
		t.Fatalf("an unsubscribed member must resolve: %v", err)
	}
	if got.ID != "acct_01HOBBY" || got.HasActiveSubscription {
		t.Errorf("got %+v, want the unsubscribed hobby membership reported as such", got)
	}
}

// TestLookupAccount_RefusesANonMemberAndAnUnreachableList holds both negative
// arms. They are DIFFERENT failures and must not collapse into one: "not a
// member" is an answer, "could not check" is the absence of one, and a caller
// that treated the second as the first would refuse a legitimate account
// whenever the network blipped.
func TestLookupAccount_RefusesANonMemberAndAnUnreachableList(t *testing.T) {
	t.Run("a non-member", func(t *testing.T) {
		serveAccounts(t, twoAccountsBody)
		_, err := LookupAccount(context.Background(), "someone-elses-account")
		if err == nil {
			t.Fatal("want a refusal, got nil")
		}
		for _, want := range []string{"not a member", "someone-elses-account", "knowledge accounts"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
		}
	})

	t.Run("an unreachable list", func(t *testing.T) {
		serveAccounts(t, `{"accounts":null,"count":3}`)
		_, err := LookupAccount(context.Background(), "acme")
		if err == nil {
			t.Fatal("want a refusal on an incomplete body, got nil")
		}
		if !strings.Contains(err.Error(), "nothing was changed") {
			t.Errorf("error %q must say nothing was changed", err)
		}
	})
}

// TestUseAccount_IsTheCommandsOwnFlow proves the daemon entry point performs the
// same write the command does, refusals included, and reports the id it stored.
func TestUseAccount_IsTheCommandsOwnFlow(t *testing.T) {
	t.Run("writes the id and reports it", func(t *testing.T) {
		serveAccounts(t, twoAccountsBody)
		path := useHomeWithConfig(t, "")
		var out strings.Builder

		stored, err := UseAccount(context.Background(), &out, "acme")
		if err != nil {
			t.Fatalf("UseAccount: %v", err)
		}
		if stored != "acct_01ACME" {
			t.Errorf("stored = %q, want the UUID rather than the slug", stored)
		}
		body := readConfig(t, path)
		if !strings.Contains(body, "acct_01ACME") {
			t.Errorf("config does not carry the id:\n%s", body)
		}
		if !strings.Contains(body, "# a comment the writer must preserve") {
			t.Errorf("the write must preserve the rest of the file:\n%s", body)
		}
		if !strings.Contains(out.String(), "Now using account acme") {
			t.Errorf("confirmation = %q", out.String())
		}
	})

	t.Run("keeps the unsubscribed refusal and writes nothing", func(t *testing.T) {
		serveAccounts(t, twoAccountsBody)
		path := useHomeWithConfig(t, "acct_01ACME")
		before := readConfig(t, path)
		var out strings.Builder

		_, err := UseAccount(context.Background(), &out, "hobby")
		if err == nil {
			t.Fatal("want the unsubscribed refusal, got nil")
		}
		if !strings.Contains(err.Error(), "no active subscription") {
			t.Errorf("error %q is not the subscription refusal", err)
		}
		if got := readConfig(t, path); got != before {
			t.Errorf("a refused account use must leave the config byte-identical:\nbefore:\n%s\nafter:\n%s", before, got)
		}
		if out.Len() != 0 {
			t.Errorf("a refused account use must print no confirmation, got %q", out.String())
		}
	})

	t.Run("an unreachable list fails rather than writing optimistically", func(t *testing.T) {
		serveAccounts(t, `{"accounts":null,"count":1}`)
		path := useHomeWithConfig(t, "acct_01ACME")
		before := readConfig(t, path)
		var out strings.Builder

		if _, err := UseAccount(context.Background(), &out, "acme"); err == nil {
			t.Fatal("want a refusal, got nil")
		}
		if got := readConfig(t, path); got != before {
			t.Error("nothing may be written when the membership list could not be read")
		}
	})
}

// TestUseAccount_ReadsTheStoredIDFromTheFile is the reason the read-back is not
// taken from the process's cached selection: that cache is a TTL window old by
// design, so a caller who just switched would be told the previous account.
func TestUseAccount_ReadsTheStoredIDFromTheFile(t *testing.T) {
	serveAccounts(t, twoAccountsBody)
	path := useHomeWithConfig(t, "acct_01HOBBY")
	var out strings.Builder

	stored, err := UseAccount(context.Background(), &out, "acme")
	if err != nil {
		t.Fatalf("UseAccount: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), stored) {
		t.Errorf("reported %q, which the config file does not carry:\n%s", stored, raw)
	}
	if stored == "acct_01HOBBY" {
		t.Error("reported the PREVIOUS account: the read-back must come from the file, not a cache")
	}
}
