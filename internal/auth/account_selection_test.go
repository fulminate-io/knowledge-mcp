// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// ttlTestWindow is the TTL every clock-driven selection test is built on. It is
// deliberately far longer than any run of this suite: the fake clock is the only
// thing that can cross it, so an assertion that lands on the far side of the
// window is proof that the code consulted the injected clock, and an assertion
// on the near side cannot be spoiled by a slow machine.
const ttlTestWindow = time.Hour

// fakeSelectionClock is a controllable clock for AccountSelection's TTL. Reads
// happen on the caller's goroutine under the selection mutex, advances on the
// test goroutine; the mutex here keeps the two honest under -race.
type fakeSelectionClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeSelectionClock() *fakeSelectionClock {
	return &fakeSelectionClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeSelectionClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// advancePastTTL moves the clock beyond the cache window, so the next read
// re-reads the config. Every crossing in these tests is this one.
func (c *fakeSelectionClock) advancePastTTL() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(2 * ttlTestWindow)
}

// newClockedSelection builds a selection over path whose TTL is measured
// against a fake clock the caller advances.
func newClockedSelection(t *testing.T, path string) (*AccountSelection, *fakeSelectionClock) {
	t.Helper()
	clk := newFakeSelectionClock()
	sel := NewAccountSelection(path, ttlTestWindow)
	sel.setClockForTest(clk.Now)
	return sel, clk
}

// TestAccountSelection_CachesAndRefreshes pins the three cache behaviors: a
// stored selection is served, an out-of-process rewrite is picked up once the
// TTL expires, and an unreadable config holds the last known value rather than
// flapping the routing identity to empty.
//
// The TTL boundary is crossed by advancing a fake clock, never by sleeping. A
// sleep-and-hope test asserts how fast the machine is: this test failed on a
// loaded CI runner because more than a 20ms TTL elapsed between two adjacent
// reads and the cache correctly expired.
func TestAccountSelection_CachesAndRefreshes(t *testing.T) {
	const first = "acct_01FIRSTFIRSTFIRSTFIRSTFI"
	const second = "acct_01SECONDSECONDSECONDSEC"

	ctx := context.Background()
	dir := t.TempDir()
	path := seedSelection(t, dir, first)

	sel, clk := newClockedSelection(t, path)

	if got := sel.ID(ctx); got != first {
		t.Fatalf("ID() = %q, want %q", got, first)
	}

	// Out-of-process change; the clock has not moved, so the cached value
	// stands however long the machine took to get here.
	if err := config.WriteSelectedAccountID(path, second); err != nil {
		t.Fatalf("rewrite selection: %v", err)
	}
	if got := sel.ID(ctx); got != first {
		t.Errorf("inside TTL: ID() = %q, want the cached %q", got, first)
	}

	// Past the TTL, the new value is picked up — the known-positive that
	// proves the reader is live and the hold-last-known case below is real.
	// Only the fake clock can reach here: no run of this suite spans an hour.
	clk.advancePastTTL()
	if got := sel.ID(ctx); got != second {
		t.Errorf("after TTL: ID() = %q, want %q", got, second)
	}

	// The config becomes unreadable (malformed TOML): hold the last known
	// value rather than reporting "no selection". This leg is the source of
	// the expected "reading the selected Fulminate account failed" WARN.
	if err := os.WriteFile(path, []byte("this is not = = valid toml ["), 0o600); err != nil {
		t.Fatalf("corrupt config: %v", err)
	}
	clk.advancePastTTL()
	if got := sel.ID(ctx); got != second {
		t.Errorf("unreadable config: ID() = %q, want the last known %q", got, second)
	}
	// And it stays held on a subsequent expiry, without hammering or flapping.
	clk.advancePastTTL()
	if got := sel.ID(ctx); got != second {
		t.Errorf("unreadable config (second expiry): ID() = %q, want %q", got, second)
	}

	// A machine with no selection at all reads as empty.
	none, _ := newClockedSelection(t, seedSelection(t, t.TempDir(), ""))
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

	// Outcome 1: no selection stored — no header, no error.
	unset, _ := newClockedSelection(t, seedSelection(t, t.TempDir(), ""))
	got, err := unset.IDForRequest(ctx)
	if err != nil {
		t.Fatalf("unset: unexpected error %v", err)
	}
	if got != "" {
		t.Errorf("unset: id = %q, want empty", got)
	}

	// Outcome 2: a stored, unrejected selection is stamped.
	path := seedSelection(t, t.TempDir(), id)
	sel, clk := newClockedSelection(t, path)
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
	fresh, _ := newClockedSelection(t, seedSelection(t, t.TempDir(), id))
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
	clk.advancePastTTL()
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
