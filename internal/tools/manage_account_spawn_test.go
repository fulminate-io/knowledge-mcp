// SPDX-License-Identifier: Apache-2.0

// manage_account_spawn_test.go — the rows behind "the account operations restart
// and spawn NOTHING", split out of manage_account_test.go for the file-length
// cap. Three gates live here because the defect is a daemon that signals its own
// pid on every account switch and no behavioral test can see it: the arms' own
// audit log (the sanctioned path), a census over the arms' source (the direct
// path), and, in the checks graph, a corpus check carrying the same shape
// durably.

package tools

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/cli"
)

// recordAccountArmActions installs the audit sink and returns the log.
func recordAccountArmActions(t *testing.T) *[]string {
	t.Helper()
	var actions []string
	prior := accountArmAudit
	accountArmAudit = func(action string) { actions = append(actions, action) }
	t.Cleanup(func() { accountArmAudit = prior })
	return &actions
}

// TestAccountUse_TakesExactlyOneOutboundActionAndRestartsNothing is requirement
// 3's row (d) on the SANCTIONED path: the arm's whole outbound action list is
// one selection write.
//
// THE ASSERTION IS OVER THE WHOLE LIST, not over the absence of one name. An
// absence assertion passes for a spawn nobody thought to name; an equality over
// the list fails for any action at all that is not the one this arm exists to
// perform. Research probe 3 on this ticket showed why the specific action being
// guarded against is fatal rather than untidy: a restart issued from inside the
// daemon resolves the daemon-port owner and signals its own pid, so a re-added
// restart would kill the daemon on every account switch.
func TestAccountUse_TakesExactlyOneOutboundActionAndRestartsNothing(t *testing.T) {
	deps, _ := newAccountDeps(t)
	path := seedConfig(t)
	stubUseAccount(t, path, nil)
	harnessSession(t, "")
	actions := recordAccountArmActions(t)

	res := accountManageCall(t, deps, `{"operation":"account_use","account":"acct_01BBBBBBBBBBBBBBBB"}`)
	require.False(t, res.IsError, "account_use: %s", textBodyTools(res))

	assert.Equal(t, []string{"selection-write"}, *actions,
		"account_use takes ONE outbound action; anything else here is a process or network action nobody asked for")

	// KNOWN POSITIVE on the same instrument in the same run: the sink DOES
	// record, so the equality above is evidence rather than a recorder that was
	// never wired. Without this a broken sink and a clean arm read identically.
	auditAccountArm("process-spawn")
	assert.Equal(t, []string{"selection-write", "process-spawn"}, *actions,
		"the audit sink must record what it is given, or the assertion above proves nothing")
}

// TestAccountForSession_TakesOnlyItsTwoOutboundActions is the same closure for
// the sibling arm: a membership lookup and a binding write, in that order, and
// nothing else.
func TestAccountForSession_TakesOnlyItsTwoOutboundActions(t *testing.T) {
	deps, _ := newAccountDeps(t)
	harnessSession(t, "harness-123")
	stubAccountLookup(t, map[string]cli.AccountMembership{
		"acme": {ID: "acct_01ACME", Slug: "acme", HasActiveSubscription: true},
	}, nil)
	actions := recordAccountArmActions(t)

	res := accountManageCall(t, deps, `{"operation":"account_for_session","account":"acme"}`)
	require.False(t, res.IsError, "bind: %s", textBodyTools(res))
	assert.Equal(t, []string{"membership-lookup", "binding-write"}, *actions)
}

// TestAccountArms_SpawnNoProcess is the gate for the UNSANCTIONED path: an
// author who spawns a child directly records nothing in the audit log above, so
// the arms' own source is what has to be read.
//
// It parses the file rather than matching text, so a call spelled across a line
// break or behind an alias is still seen, and it fails in `go test` — the corpus
// check that covers the same shape is durable across the repo but is a separate
// runner, and this defect is bad enough to want both.
func TestAccountArms_SpawnNoProcess(t *testing.T) {
	const armFile = "manage_account.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, armFile, nil, 0)
	require.NoError(t, err)

	// Every package-qualified call these arms must never make. exec spawns;
	// syscall and os.Process signal; the launch agents are the platform restart
	// paths the CLI uses and the daemon must not.
	banned := map[string]map[string]bool{
		"exec":    {"Command": true, "CommandContext": true, "LookPath": true},
		"syscall": {"Kill": true, "Exec": true},
		"os":      {"StartProcess": true, "FindProcess": true},
	}
	var offenders []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if banned[pkg.Name][sel.Sel.Name] {
			offenders = append(offenders,
				fmt.Sprintf("%s:%d %s.%s", armFile, fset.Position(call.Pos()).Line, pkg.Name, sel.Sel.Name))
		}
		return true
	})
	assert.Empty(t, offenders,
		"the account arms must spawn and signal NOTHING: a restart issued from inside the daemon "+
			"resolves the daemon-port owner and signals this very process, so it is a self-kill on every "+
			"account switch. Sites: %v", offenders)

	// KNOWN POSITIVE: the same walk over a snippet that DOES spawn reports it,
	// so the empty result above is a reading of this file rather than a matcher
	// that can never fire.
	probe, err := parser.ParseFile(token.NewFileSet(), "probe.go",
		"package tools\nimport \"os/exec\"\nfunc restart() { _, _ = exec.Command(\"/bin/echo\", \"restarting\").Output() }\n", 0)
	require.NoError(t, err)
	found := false
	ast.Inspect(probe, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && banned[pkg.Name][sel.Sel.Name] {
					found = true
				}
			}
		}
		return true
	})
	assert.True(t, found, "the census must find a spawn when one is there, or its empty result means nothing")
}
