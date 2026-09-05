// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "testing"

// TestHandoffSysProcAttrIsAlwaysSupplied pins the PLATFORM-NEUTRAL half of the
// handoff's spawn contract: whatever the current platform's isolation mechanism
// is, the handoff child is never spawned with the process attributes left nil.
//
// IT COMPILES ON EVERY RELEASE TARGET, which is the point of it living in an
// untagged file. The two platform arms below it assert the mechanism — Setpgid
// on unix, CREATE_NEW_PROCESS_GROUP on windows — and neither of those
// assertions can be written in a file the other platform compiles. This test is
// what remains sayable in both, and it is not nothing: a nil SysProcAttr is
// exactly the shape the pre-fix code produced on any platform whose isolation
// arm was forgotten.
func TestHandoffSysProcAttrIsAlwaysSupplied(t *testing.T) {
	attr := handoffSysProcAttr()
	if attr == nil {
		t.Fatal("handoffSysProcAttr returned nil; the handoff child would be spawned sharing the parent's job/process group and killed by the very stop it issues")
	}
	if !handoffAttrIsolatesChild(attr) {
		t.Error("handoffSysProcAttr returned attributes that do not isolate the child from this process's group on this platform")
	}
}
