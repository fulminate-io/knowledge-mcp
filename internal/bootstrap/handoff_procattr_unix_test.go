// SPDX-License-Identifier: Apache-2.0

//go:build unix

package bootstrap

import (
	"syscall"
	"testing"
)

// handoffAttrIsolatesChild reports whether attr gives the spawned child its own
// process group on unix. It is the platform-specific half of the neutral
// contract asserted in handoff_procattr_test.go, and the assertion
// client_update_restart_test.go makes about the spawned handoff child — written
// once here so the untagged callers stay compilable on every release target.
func handoffAttrIsolatesChild(attr *syscall.SysProcAttr) bool {
	return attr != nil && attr.Setpgid
}

// TestHandoffSysProcAttrUnixSetsSetpgid pins HALF ONE of the survival fix on
// the platform where it was MEASURED: a child forked the ordinary way shares
// the parent's process group, and `launchctl bootout` — the exact call the unit
// stop makes — kills such a child. Setpgid gives the child its own process
// GROUP and it survives.
//
// SETPGID, NOT SETSID, and the distinction is asserted rather than assumed: a
// session leader is what the macOS runningboardd caveat governs, and a process
// group is not one. There is no Setsid field to check here, so what this test
// can do is fail loudly if the flag that IS load-bearing goes missing.
func TestHandoffSysProcAttrUnixSetsSetpgid(t *testing.T) {
	attr := handoffSysProcAttr()
	if attr == nil {
		t.Fatal("handoffSysProcAttr returned nil on unix; the handoff child would share this process's group")
	}
	if !attr.Setpgid {
		t.Error("handoffSysProcAttr did not set Setpgid; the handoff child would be killed by the unit stop it performs, leaving new binaries on disk and no daemon running")
	}
}
