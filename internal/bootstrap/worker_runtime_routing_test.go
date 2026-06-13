// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
	"github.com/fulminate-io/knowledge-mcp/internal/auth"
	"github.com/fulminate-io/knowledge-mcp/internal/dream"
	"github.com/fulminate-io/knowledge-mcp/internal/graphclient"
	"github.com/fulminate-io/knowledge-mcp/internal/workercrud"
)

// TestWorkerRuntimeList_LoggedInRoutesCloud (FAILS-WHEN-ABSENT) codifies the
// login-routing contract for the worker runtime: when the router reports
// LoggedIn=true (cloud-routed daemon, no local server), the dream Runner's
// worker-list path (Registry.All → workercrud.Client.List → router.Execute) must
// route to the cloud engine, NOT dial the local :15022 GraphClient. This is the
// exact registry object buildRuntime
// constructs (dream.NewRegistry(workercrud.New(c.router)) at dream.go:438), with
// c.router passed by wireWorkerRuntime (dream.go:456). The test pins the routing so
// a future reorder cannot silently reintroduce the local dial. No production code
// changes — verify-only.
func TestWorkerRuntimeList_LoggedInRoutesCloud(t *testing.T) {
	localURL, localEng := startCountingEngine(t)
	cloudURL, cloudEng := startCountingEngine(t)
	localGC := graphclient.NewGraphClientForURL(localURL)

	// Logged-in keychain: a refresh token present → AuthState.IsLoggedIn=true →
	// Router.pick routes cloud (mirrors router_keychain_test.go).
	store := newFakeAuthStore()
	require.NoError(t, store.Set(context.Background(), auth.KeyRefreshToken, "frt-fresh"))
	as := auth.NewAuthState(store, time.Millisecond)
	router := graphclient.NewRouter(localGC, cloudURL, staticTokenSource{tok: "tok-cloud"}, as)
	require.True(t, router.LoggedIn(context.Background()), "precondition: router reports logged-in")

	// The SAME registry buildRuntime wires: a dream.Registry backed by the
	// workercrud client over the login-aware router. Registry.All is the worker-list
	// path the runtime drives.
	reg := dream.NewRegistry(workercrud.New(router))
	_, err := reg.All(context.Background())
	require.NoError(t, err)

	assert.Equal(t, int32(1), cloudEng.execute.Load(),
		"a logged-in worker List must route to the CLOUD engine")
	assert.Equal(t, int32(0), localEng.execute.Load(),
		"a logged-in worker List must NOT dial the local :15022 engine")
}

// compile-time guard: *graphclient.Router satisfies the Execute carrier the
// workercrud client + dream registry consume (the routing seam under test).
var _ interface {
	Execute(context.Context, *knowledgev1.ExecuteRequest) (*knowledgev1.ExecuteResponse, error)
} = (*graphclient.Router)(nil)
