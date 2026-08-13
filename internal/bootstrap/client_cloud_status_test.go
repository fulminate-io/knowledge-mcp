// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
)

// THIS TEST TOUCHES NO CLOUD AND NO CREDENTIAL STORE, by construction rather
// than by convention. Every case builds a Router directly with the same
// constructor constructClient uses, and the Router's cloud client is built
// LAZILY inside pick/ensureCloud — which nothing here calls — so no dial can
// occur. The auth state is backed by noopAuthStore{}, the package's own zero-IO
// stub whose Get always returns auth.ErrNotFound, so no keychain or credentials
// file is opened. No token, no server, no network, no fixture process.

// newCloudStatusClient builds the minimal *client CloudStatusInfo reads: a
// Router carrying the machine-auth flag and an AuthState over a store that holds
// nothing. The empty cloudURL is deliberate — the endpoint is never dialed, and
// leaving it blank makes that structural rather than a promise.
func newCloudStatusClient(machineAuth bool) *client {
	authState := auth.NewAuthState(noopAuthStore{}, 0)
	return &client{
		authState: authState,
		router:    graphclient.NewRouterWithMachineAuth(nil, "", auth.StaticTokenSource{}, authState, machineAuth),
	}
}

// TestCloudStatusInfo_MachineTokenReportsCloud is the regression proof for the
// defect: a client authenticated by a machine bearer routes every operation to
// cloud (Router.pick short-circuits on machineAuth) while holding NO credential
// -store login, and manage(status) used to render the LOCAL daemon for it —
// because CloudStatusInfo asked the auth state instead of the router.
//
// The two cases share one store — empty in both — so the only variable is the
// machine-auth flag, and the false case is the known-positive control that keeps
// the true case honest: without it, an accessor hard-wired to return true would
// pass.
func TestCloudStatusInfo_MachineTokenReportsCloud(t *testing.T) {
	t.Run("machine token, no store login", func(t *testing.T) {
		loggedIn, host := newCloudStatusClient(true).CloudStatusInfo()
		if !loggedIn {
			t.Error("CloudStatusInfo() = false for a machine-token client; " +
				"it routes every op to cloud, so status must report the cloud backend")
		}
		if host == "" {
			t.Error("CloudStatusInfo() returned an empty host; status renders it")
		}
	})

	t.Run("no machine token, no store login", func(t *testing.T) {
		if loggedIn, _ := newCloudStatusClient(false).CloudStatusInfo(); loggedIn {
			t.Error("CloudStatusInfo() = true with neither a machine token nor a " +
				"store login; the local-daemon path is correct here")
		}
	})
}

// TestCloudStatusInfo_MatchesRouterRouting pins the property the fix is really
// about: the status accessor and the per-RPC routing decision must not be able
// to disagree. Asserting equality against Router.LoggedIn — the predicate
// Router.pick itself applies — is what makes "status says local while calls go
// to cloud" unrepresentable, rather than merely fixed for today's inputs.
func TestCloudStatusInfo_MatchesRouterRouting(t *testing.T) {
	for _, machineAuth := range []bool{true, false} {
		c := newCloudStatusClient(machineAuth)
		loggedIn, _ := c.CloudStatusInfo()
		if want := c.router.LoggedIn(t.Context()); loggedIn != want {
			t.Errorf("machineAuth=%v: CloudStatusInfo()=%v but Router.LoggedIn()=%v — "+
				"status and routing disagree", machineAuth, loggedIn, want)
		}
	}
}

// TestCloudStatusInfo_NilRouterDegrades keeps the degraded-fixture tolerance the
// sibling accessors carry: a *client built without a router (as several tests in
// this package do) must answer false rather than panic.
func TestCloudStatusInfo_NilRouterDegrades(t *testing.T) {
	loggedIn, host := (&client{}).CloudStatusInfo()
	if loggedIn {
		t.Error("CloudStatusInfo() = true on a router-less client")
	}
	if host == "" {
		t.Error("CloudStatusInfo() must still name the host on the degraded path")
	}
}
