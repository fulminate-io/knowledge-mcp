// SPDX-License-Identifier: Apache-2.0

// Client-side PropagationLoop construction.
//
// The reflective scheduler runs in the MCP stdio client — the hourly cluster
// detection + valence/magnitude propagation loop. wirePropagationRuntime
// mirrors wireWorkerRuntime: a single helper that constructs the
// long-lived runtime object, attaches it to *client, and starts it.
// runMCPMode wires the deferred Stop with the same nil-safety
// convention dream.Runner.Stop uses.
//
// Per T1 (reviewer round 2) PropagationLoop holds *graphclient.GraphClient
// DIRECTLY — no client-side store-shaped wrapper. Every read and write
// the loop performs is a wire call through gc.

package bootstrap

import (
	clientthought "github.com/fulminate-io/knowledge-mcp/internal/thought"
)

// wirePropagationRuntime constructs the client-side PropagationLoop and
// wires it into the *client. REUSES c.client (single TCP connection)
// and assigns the returned loop to c.propLoop. Calls Start so the loop
// begins ticking immediately.
//
// Construction cannot fail today, but the caller logs any future error
// modes the same way wireWorkerRuntime does — see runMCPMode in mcp.go.
func wirePropagationRuntime(c *client) {
	loop := clientthought.NewPropagationLoop(c.client)
	c.propLoop = loop
	loop.Start()
}
