// SPDX-License-Identifier: Apache-2.0

// Package provider routes between concrete Backend adapters via a closed
// switch. It is split out from domains/backends to avoid an import cycle:
// adapters (e.g. domains/backends/linear) import domains/backends for the
// Backend interface and the value types, and this provider package imports
// both domains/backends and the adapter sub-packages to wire them up.
//
// # Closed-switch policy
//
// Adding a new backend means adding ONE arm to Available, and nothing else.
// There is intentionally no init-time registry, no Register() call, and no
// mutex — backends are a closed set of operator-facing integrations, not a
// third-party plugin extension point. Default tie-break is the order arms
// appear in Available: first Enabled() wins.
package provider

import (
	"github.com/fulminate-io/knowledge-mcp/internal/backends"
	"github.com/fulminate-io/knowledge-mcp/internal/backends/linear"
)

// Available returns every backend whose env-var credentials are currently set.
// Order is the closed-switch order — first arm wins for Default() when
// multiple backends are simultaneously configured.
//
// Adding a new backend means adding ONE arm here. There is no init-time
// registry. Backends are a closed set of operator-facing integrations, not a
// third-party plugin extension point.
func Available() []backends.Backend {
	var out []backends.Backend
	if linear.Enabled() {
		out = append(out, linear.New())
	}
	// Future arms will look like:
	//   if jira.Enabled()   { out = append(out, jira.New())   }
	//   if github.Enabled() { out = append(out, github.New()) }
	return out
}

// Default returns the first backend in Available(), or nil if none are
// configured. Multi-backend tie-break is the closed-switch order in
// Available — first Enabled() wins.
func Default() backends.Backend {
	avail := Available()
	if len(avail) == 0 {
		return nil
	}
	return avail[0]
}

// ByName returns the named backend if its credentials are set, or nil if
// either the name is unknown or the backend is not currently configured.
// Used by T3 to route per-node update/delete dispatch via the value of the
// `backend` metadata key on the local node.
func ByName(name string) backends.Backend {
	for _, b := range Available() {
		if b.Name() == name {
			return b
		}
	}
	return nil
}
