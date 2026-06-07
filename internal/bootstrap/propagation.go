// SPDX-License-Identifier: Apache-2.0

// Client-side PropagationLoop construction.
//
// The reflective scheduler runs in the serve daemon's client — the hourly
// cluster detection + valence/magnitude propagation loop. wirePropagationRuntime
// mirrors wireWorkerRuntime: a single helper that constructs the
// long-lived runtime object, attaches it to *client, and starts it.
// buildClient's cleanup closure (daemon.go) wires the deferred Stop with the
// same nil-safety convention dream.Runner.Stop uses.
//
// PropagationLoop holds the Execute-only thought.Caller (passed c.router) —
// no client-side store-shaped wrapper. Every read and write the loop performs
// is a wire call through the routed caller, so propagation routes
// cloud-when-logged-in (no local-server probe in a cloud-only daemon).

package bootstrap

import (
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// wirePropagationRuntime constructs the client-side PropagationLoop and
// wires it into the *client. Passes the login-aware c.router (so the loop's
// reads/writes route cloud-when-logged-in) and assigns the returned loop to
// c.propLoop. Calls Start so the loop begins ticking immediately.
//
// Construction cannot fail today, but the caller logs any future error
// modes the same way wireWorkerRuntime does — see buildClient in daemon.go.
func wirePropagationRuntime(c *client) {
	loop := clientthought.NewPropagationLoop(c.router)
	c.propLoop = loop
	loop.Start()
}
