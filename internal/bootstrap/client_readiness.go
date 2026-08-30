// SPDX-License-Identifier: Apache-2.0

package bootstrap

// client_readiness.go holds the per-subsystem readiness signal (bind-first startup): the
// bind-first daemon binds the HTTP MCP listener first, then wires the
// propagation / pipeline runtimes in a background goroutine. The atomic.Bool
// flags + their mark*/Reader accessors distinguish that wiring window from a
// permanent boot degrade, so the intercept guards emit "daemon still starting"
// during the window and the shutdown drain only Stops a published handle. The
// flag fields themselves live on the client struct (client.go).

// markPropReady / markPipelineReady latch the corresponding
// readiness flag true at the end of each background wiring stage. They are
// called from wireRuntimesBackground (daemon.go) AFTER the subsystem field
// (c.propLoop / c.pipeline) is assigned, so the atomic Store
// publishes the fully-wired handle to any reader that observes Ready()==true.
func (c *client) markPropReady()     { c.propReady.Store(true) }
func (c *client) markPipelineReady() { c.pipelineReady.Store(true) }

// PropReady / PipelineReady report whether the corresponding background wiring
// stage has completed. False during the bind-first wiring window (bind-first
// startup); the intercept guards consult these to emit a "daemon still
// starting" error instead of dereferencing a not-yet-wired handle. The atomic
// Load pairs with the Store in mark*Ready to give the consumer the
// happens-before guarantee that a true result implies the wired handle is
// visible. Satisfies tools.ClientDeps.
func (c *client) PropReady() bool     { return c.propReady.Load() }
func (c *client) PipelineReady() bool { return c.pipelineReady.Load() }
