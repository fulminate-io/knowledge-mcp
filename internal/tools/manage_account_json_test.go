// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/config"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

const (
	statusAccountHeader = "22222222-2222-4222-8222-222222222222"
	statusAccountGlobal = "11111111-1111-4111-8111-111111111111"
)

// statusArms returns the three JSON arms of manage(status) by name. The third
// is the one that emits neither the local-daemon block nor the cloud one, which
// is exactly why the request-account fields are asserted on it separately — a
// web page reads their ABSENCE as "this daemon predates the feature".
// notRunningDeps is the harness-session tests' fixture for that arm
// (manage_harness_session_test.go); both features need the same one.
func statusArms() map[string]ClientDeps {
	return map[string]ClientDeps{
		"cloud":         newFullDaemonDeps(true),
		"local running": newFullDaemonDeps(false),
		"not running":   notRunningDeps(),
	}
}

// selectAccountForTest points the process-wide selection at a config under the
// test's own temp dir — never the operator's — and restores the suite's
// neutralized selection afterwards. The id is statusAccountGlobal: every row in
// this package that needs a selection needs THE one the fixtures call global,
// and a parameter no caller varies would read as a choice nobody makes.
func selectAccountForTest(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, config.WriteSelectedAccountID(path, statusAccountGlobal))
	t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(path, 0)))
}

// statusJSON runs one arm and returns its decoded JSON body. ctx leads, as it
// does on every function in this tree that takes one.
func statusJSON(ctx context.Context, t *testing.T, deps ClientDeps) map[string]any {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(textBodyTools(handleServerStatus(ctx, deps, "json"))), &got))
	return got
}

// TestHandleServerStatus_ReportsTheRequestAccount is requirement 3's status
// row, on ALL THREE json arms. account/account_source describe the CALLING
// REQUEST's resolution, which exists whether or not a local graph server is up,
// so every arm carries them with the same values for the same request.
func TestHandleServerStatus_ReportsTheRequestAccount(t *testing.T) {
	selectAccountForTest(t)

	t.Run("header-bound request", func(t *testing.T) {
		ctx := graphclient.WithRequestAccount(opCtx(), statusAccountHeader, graphclient.AccountSourceHeader)
		for name, deps := range statusArms() {
			got := statusJSON(ctx, t, deps)
			assert.Equal(t, statusAccountHeader, got["account"], "%s arm must report the request's account", name)
			assert.Equal(t, graphclient.AccountSourceHeader, got["account_source"], "%s arm", name)
		}
	})

	t.Run("unbound request falls to the global selection", func(t *testing.T) {
		for name, deps := range statusArms() {
			got := statusJSON(opCtx(), t, deps)
			assert.Equal(t, statusAccountGlobal, got["account"], "%s arm", name)
			assert.Equal(t, graphclient.AccountSourceGlobal, got["account_source"], "%s arm", name)
		}
	})

	t.Run("no selection at all reports none", func(t *testing.T) {
		t.Cleanup(auth.SetSelectedAccountForTest(auth.NewAccountSelection(filepath.Join(t.TempDir(), "config"), 0)))
		for name, deps := range statusArms() {
			got := statusJSON(opCtx(), t, deps)
			assert.Empty(t, got["account"], "%s arm", name)
			assert.Equal(t, graphclient.AccountSourceNone, got["account_source"], "%s arm", name)
			assert.Contains(t, got, "account", "%s arm must emit the key even with no account", name)
		}
	})

	t.Run("an ignored header reports no account, with the reason", func(t *testing.T) {
		// A logged-out daemon serves the request locally; reporting the
		// header's account here is the wrong-cause claim a web page renders as
		// a cloud account's name over local data.
		ctx := graphclient.WithIgnoredAccountHeader(opCtx(), statusAccountHeader, graphclient.AccountReasonLoggedOut)
		for name, deps := range statusArms() {
			got := statusJSON(ctx, t, deps)
			assert.Empty(t, got["account"], "%s arm must claim no account", name)
			assert.Equal(t, graphclient.AccountSourceNone, got["account_source"], "%s arm", name)
			assert.Equal(t, graphclient.AccountReasonLoggedOut, got["account_reason"],
				"%s arm must say what happened to the header", name)
		}
	})

	t.Run("a resolved account carries no reason", func(t *testing.T) {
		ctx := graphclient.WithRequestAccount(opCtx(), statusAccountHeader, graphclient.AccountSourceHeader)
		for name, deps := range statusArms() {
			got := statusJSON(ctx, t, deps)
			assert.Empty(t, got["account_reason"], "%s arm: a clean resolution explains nothing", name)
			assert.Contains(t, got, "account_reason", "%s arm must emit the key even when empty", name)
		}
	})

	t.Run("the not_running arm still emits them", func(t *testing.T) {
		// Named separately because this arm emits no local-daemon block at all,
		// and it is the arm a pre-feature daemon is detected by.
		got := statusJSON(opCtx(), t, notRunningDeps())
		assert.Equal(t, "not_running", got["status"])
		assert.Contains(t, got, "account_source")
		assert.NotContains(t, got, "pid", "the not_running arm still emits no local-daemon block")
	})
}

// TestHandleServerStatus_TextArmReportsTheAccountAndNothingElseChanges is the
// text-arm pin, CONVERTED by the session-binding change.
//
// It asserted that the text body was BYTE-IDENTICAL with and without an account
// header — "keeps the human render out of a JSON-only change" — which was true
// while the account fields were json-only. The session-binding ticket's
// requirement 6 asks manage(status) to report the effective account and its
// source, and the harness-session line already sets the precedent that the text
// arm carries per-request identity, so the account is rendered there too.
//
// WHAT T3's TEST WAS PROTECTING IS KEPT, and is the whole of this one: the
// account header changes the ACCOUNT LINE and nothing else. Every other line of
// every arm is still byte-identical, so a future account change that leaks into
// the coverage table or the doctor block is a red here.
func TestHandleServerStatus_TextArmReportsTheAccountAndNothingElseChanges(t *testing.T) {
	selectAccountForTest(t)
	bound := graphclient.WithRequestAccount(opCtx(), statusAccountHeader, graphclient.AccountSourceHeader)

	for name, deps := range statusArms() {
		unbound := textBodyTools(handleServerStatus(opCtx(), deps, ""))
		withHeader := textBodyTools(handleServerStatus(bound, deps, ""))

		assert.Contains(t, withHeader, "Account: "+statusAccountHeader+" (header)",
			"%s arm: the text render reports the request's account", name)
		assert.Contains(t, unbound, "Account: "+statusAccountGlobal+" (global)",
			"%s arm: and the selection when no header decided it", name)

		// …and that ONE line is the whole difference.
		assert.Equal(t,
			withoutAccountLine(withHeader), withoutAccountLine(unbound),
			"%s arm: the account header must change the account line and nothing else", name)
	}
}

// withoutAccountLine drops the rendered account line, so two bodies can be
// compared for everything else.
func withoutAccountLine(body string) string {
	var kept []string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Account: ") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
