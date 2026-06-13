// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "github.com/fulminate-io/knowledge-mcp/internal/hivemonitor"

// ClaimRegistry returns the client-side hive claim registry (the SAME instance
// handed to the daemon Monitor). InterceptHive Binds/Clears claims on it; the
// Monitor reads it each tick. Returns nil in test harnesses that build *client
// directly (claimRegistry unwired); the accessor's callers nil-check and the
// Registry methods are themselves nil-safe.
func (c *client) ClaimRegistry() *hivemonitor.Registry {
	return c.claimRegistry
}

// BanSet returns the client-side hive ban set (the SAME instance handed to the
// daemon Monitor). InterceptHive consults it to refuse a banned session's hive
// calls before they reach cloud. Returns nil in test harnesses that build
// *client directly; the gate nil-checks and the BanSet methods are nil-safe.
func (c *client) BanSet() *hivemonitor.BanSet {
	return c.banSet
}
