// SPDX-License-Identifier: Apache-2.0

//go:build windows

package bootstrap

import (
	"syscall"
	"testing"
)

// handoffAttrIsolatesChild reports whether attr gives the spawned child its own
// console process group on windows. It is the platform-specific half of the
// neutral contract asserted in handoff_procattr_test.go, and the assertion
// client_update_restart_test.go makes about the spawned handoff child — written
// once here so the untagged callers stay compilable on every release target.
func handoffAttrIsolatesChild(attr *syscall.SysProcAttr) bool {
	return attr != nil && attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP != 0
}

// TestHandoffSysProcAttrWindowsSetsNewProcessGroup pins the windows arm of the
// survival fix: the child is spawned into its own console process group, which
// is the mechanism that keeps a CTRL_C_EVENT or CTRL_BREAK_EVENT delivered to
// this process's group off the child.
//
// LIKE THE OTHER WINDOWS ARMS IN THIS TREE, this assertion has not been
// executed on a windows machine — CI builds the windows/amd64 client and runs
// no tests on it. What it does buy is a compile-time and, on any future windows
// test leg, a run-time statement of which flag is load-bearing, so the constant
// cannot be dropped silently the way Setpgid was on the unix side.
func TestHandoffSysProcAttrWindowsSetsNewProcessGroup(t *testing.T) {
	attr := handoffSysProcAttr()
	if attr == nil {
		t.Fatal("handoffSysProcAttr returned nil on windows; the handoff child would share this process's console process group")
	}
	if attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("handoffSysProcAttr did not set CREATE_NEW_PROCESS_GROUP (flags were %#x); a console control event aimed at this process's group would also reach the handoff child", attr.CreationFlags)
	}
}
