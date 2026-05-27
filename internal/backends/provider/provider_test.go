// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/backends"
)

// fakeBackend is a test-only stub used to prove the Backend interface is
// satisfiable by something other than the linear adapter. If a future
// amendment adds a Linear-specific method to Backend, this fake stops
// compiling — which is the canary signal that the interface has leaked.
//
// All methods return zero values; tests requiring richer behavior wrap a
// fakeBackend with their own scripted-response struct.
//
// fakeBackend is intentionally NOT injected into Available() — that would
// require a test-only registry hook, which is exactly the indirection the
// closed-switch decision rejects. It powers ONLY the compile-time
// pluggability assertion below.
type fakeBackend struct {
	name string
}

func (f *fakeBackend) Name() string                                         { return f.name }
func (f *fakeBackend) Groups(ctx context.Context) ([]backends.Group, error) { return nil, nil }
func (f *fakeBackend) SyncGroup(ctx context.Context, group string) (backends.Snapshot, error) {
	return backends.Snapshot{}, nil
}
func (f *fakeBackend) CreateProject(ctx context.Context, args backends.ProjectCreateArgs) (backends.RemoteRef, error) {
	return backends.RemoteRef{}, nil
}
func (f *fakeBackend) UpdateProject(ctx context.Context, ref backends.RemoteRef, diff backends.ProjectDiff) error {
	return nil
}
func (f *fakeBackend) ArchiveProject(ctx context.Context, ref backends.RemoteRef) error { return nil }
func (f *fakeBackend) CreateTicket(ctx context.Context, args backends.TicketCreateArgs) (backends.RemoteRef, error) {
	return backends.RemoteRef{}, nil
}
func (f *fakeBackend) UpdateTicket(ctx context.Context, ref backends.RemoteRef, diff backends.TicketDiff) error {
	return nil
}
func (f *fakeBackend) ArchiveTicket(ctx context.Context, ref backends.RemoteRef) error { return nil }

// Compile-time assertion: fakeBackend satisfies Backend. If this line stops
// compiling, the interface has gained a Linear-specific method and needs to
// either revert or have a new abstract shape added.
var _ backends.Backend = (*fakeBackend)(nil)

// TestProvider_NoBackendsWhenEnvUnset locks in the empty-by-default contract:
// when no backend env var is set, Available is empty, Default is nil, and
// ByName returns nil for every name. We explicitly clear LINEAR_API_KEY via
// t.Setenv to avoid bleed from a developer's local environment.
func TestProvider_NoBackendsWhenEnvUnset(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "")

	if got := Available(); len(got) != 0 {
		t.Errorf("Available() len = %d, want 0 with LINEAR_API_KEY unset", len(got))
	}
	if got := Default(); got != nil {
		t.Errorf("Default() = %v, want nil with LINEAR_API_KEY unset", got)
	}
	if got := ByName("anything"); got != nil {
		t.Errorf("ByName(\"anything\") = %v, want nil", got)
	}
	if got := ByName("linear"); got != nil {
		t.Errorf("ByName(\"linear\") = %v, want nil with LINEAR_API_KEY unset", got)
	}
}

// TestProvider_LinearActive locks in the env-set contract: LINEAR_API_KEY set
// → Available returns a single linear backend, Default is that backend,
// ByName("linear") returns it, ByName("unknown") returns nil.
func TestProvider_LinearActive(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "lin_api_test")

	avail := Available()
	if len(avail) != 1 {
		t.Fatalf("Available() len = %d, want 1 with LINEAR_API_KEY set", len(avail))
	}
	if got := avail[0].Name(); got != "linear" {
		t.Errorf("Available()[0].Name() = %q, want %q", got, "linear")
	}

	def := Default()
	if def == nil {
		t.Fatal("Default() = nil, want non-nil with LINEAR_API_KEY set")
	}
	if got := def.Name(); got != "linear" {
		t.Errorf("Default().Name() = %q, want %q", got, "linear")
	}

	if got := ByName("linear"); got == nil {
		t.Error("ByName(\"linear\") = nil, want non-nil with LINEAR_API_KEY set")
	} else if got.Name() != "linear" {
		t.Errorf("ByName(\"linear\").Name() = %q, want %q", got.Name(), "linear")
	}

	if got := ByName("unknown"); got != nil {
		t.Errorf("ByName(\"unknown\") = %v, want nil", got)
	}
}
