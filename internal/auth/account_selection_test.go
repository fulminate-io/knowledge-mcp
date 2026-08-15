// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fulminate-io/knowledge-mcp/internal/config"
)

// seedSelection writes a config carrying id (or no selection when id is "")
// and returns its path.
func seedSelection(t *testing.T, dir, id string) string {
	t.Helper()
	path := filepath.Join(dir, "config")
	body := "[default]\nprovider = \"anthropic\"\nmodel = \"claude-haiku-5\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if id != "" {
		if err := config.WriteSelectedAccountID(path, id); err != nil {
			t.Fatalf("seed selection: %v", err)
		}
	}
	return path
}

// TestAccountSelection_CachesAndRefreshes pins the three cache behaviors: a
// stored selection is served, an out-of-process rewrite is picked up once the
// TTL expires, and an unreadable config holds the last known value rather than
// flapping the routing identity to empty.
func TestAccountSelection_CachesAndRefreshes(t *testing.T) {
	const first = "acct_01FIRSTFIRSTFIRSTFIRSTFI"
	const second = "acct_01SECONDSECONDSECONDSEC"

	ctx := context.Background()
	dir := t.TempDir()
	path := seedSelection(t, dir, first)

	const ttl = 20 * time.Millisecond
	sel := NewAccountSelection(path, ttl)

	if got := sel.ID(ctx); got != first {
		t.Fatalf("ID() = %q, want %q", got, first)
	}

	// Out-of-process change; still inside the TTL, so the cached value stands.
	if err := config.WriteSelectedAccountID(path, second); err != nil {
		t.Fatalf("rewrite selection: %v", err)
	}
	if got := sel.ID(ctx); got != first {
		t.Errorf("inside TTL: ID() = %q, want the cached %q", got, first)
	}

	// Past the TTL, the new value is picked up — the known-positive that
	// proves the reader is live and the hold-last-known case below is real.
	time.Sleep(2 * ttl)
	if got := sel.ID(ctx); got != second {
		t.Errorf("after TTL: ID() = %q, want %q", got, second)
	}

	// The config becomes unreadable (malformed TOML): hold the last known
	// value rather than reporting "no selection".
	if err := os.WriteFile(path, []byte("this is not = = valid toml ["), 0o600); err != nil {
		t.Fatalf("corrupt config: %v", err)
	}
	time.Sleep(2 * ttl)
	if got := sel.ID(ctx); got != second {
		t.Errorf("unreadable config: ID() = %q, want the last known %q", got, second)
	}
	// And it stays held on a subsequent expiry, without hammering or flapping.
	time.Sleep(2 * ttl)
	if got := sel.ID(ctx); got != second {
		t.Errorf("unreadable config (second expiry): ID() = %q, want %q", got, second)
	}

	// A machine with no selection at all reads as empty.
	none := NewAccountSelection(seedSelection(t, t.TempDir(), ""), ttl)
	if got := none.ID(ctx); got != "" {
		t.Errorf("no selection: ID() = %q, want empty", got)
	}
}

// TestAccountSelection_IDForRequestOutcomes pins the three-outcome contract and
// the self-clearing rejection marker.
func TestAccountSelection_IDForRequestOutcomes(t *testing.T) {
	const id = "acct_01OUTCOMESOUTCOMESOUTCO"
	const other = "acct_01OTHEROTHEROTHEROTHER"

	ctx := context.Background()
	const ttl = 20 * time.Millisecond

	// Outcome 1: no selection stored — no header, no error.
	unset := NewAccountSelection(seedSelection(t, t.TempDir(), ""), ttl)
	got, err := unset.IDForRequest(ctx)
	if err != nil {
		t.Fatalf("unset: unexpected error %v", err)
	}
	if got != "" {
		t.Errorf("unset: id = %q, want empty", got)
	}

	// Outcome 2: a stored, unrejected selection is stamped.
	path := seedSelection(t, t.TempDir(), id)
	sel := NewAccountSelection(path, ttl)
	got, err = sel.IDForRequest(ctx)
	if err != nil {
		t.Fatalf("stored: unexpected error %v", err)
	}
	if got != id {
		t.Errorf("stored: id = %q, want %q", got, id)
	}

	// Outcome 3: once the gateway has rejected it, the call is refused.
	sel.MarkInvalid(id, "account_forbidden: you are not a member of this account")
	got, err = sel.IDForRequest(ctx)
	if !errors.Is(err, ErrAccountSelectionRejected) {
		t.Fatalf("rejected: err = %v, want ErrAccountSelectionRejected", err)
	}
	if got != "" {
		t.Errorf("rejected: id = %q, want empty", got)
	}
	if !strings.Contains(err.Error(), "account_forbidden") {
		t.Errorf("rejected: error %q does not carry the gateway reason", err)
	}
	if !strings.Contains(err.Error(), id) {
		t.Errorf("rejected: error %q does not name the account", err)
	}

	// Marking a DIFFERENT id does not refuse the current selection.
	fresh := NewAccountSelection(seedSelection(t, t.TempDir(), id), ttl)
	fresh.MarkInvalid(other, "not this one")
	if _, err := fresh.IDForRequest(ctx); err != nil {
		t.Errorf("marker keyed to another id refused the current selection: %v", err)
	}

	// The marker self-clears once the stored selection changes: `knowledge
	// account use <other>` in another terminal re-arms this process within one
	// TTL, with no IPC.
	if err := config.WriteSelectedAccountID(path, other); err != nil {
		t.Fatalf("rewrite selection: %v", err)
	}
	time.Sleep(2 * ttl)
	got, err = sel.IDForRequest(ctx)
	if err != nil {
		t.Fatalf("after switching accounts: unexpected error %v", err)
	}
	if got != other {
		t.Errorf("after switching accounts: id = %q, want %q", got, other)
	}
}

// TestSetSelectedAccountForTest_RestoresSingleton pins the test seam: the
// installed selection is what SelectedAccount serves, and the restore closure
// puts the prior instance back.
func TestSetSelectedAccountForTest_RestoresSingleton(t *testing.T) {
	prior := SelectedAccount()

	installed := NewAccountSelection(seedSelection(t, t.TempDir(), "acct_01SEAMSEAMSEAMSEAMSEAMS"), time.Second)
	restore := SetSelectedAccountForTest(installed)
	if SelectedAccount() != installed {
		t.Error("SelectedAccount did not serve the installed test selection")
	}
	restore()
	if SelectedAccount() != prior {
		t.Error("restore closure did not put the prior selection back")
	}
}
