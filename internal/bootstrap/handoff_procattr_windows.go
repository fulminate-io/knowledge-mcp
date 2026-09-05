// SPDX-License-Identifier: Apache-2.0

//go:build windows

package bootstrap

import "syscall"

// handoffSysProcAttr returns the process attributes the restart handoff child
// is spawned with on windows.
//
// WHY THIS FILE EXISTS AT ALL: syscall.SysProcAttr is a DIFFERENT STRUCT on
// every platform, and the unix arm's Setpgid field does not exist here. READ
// FROM THE PINNED TOOLCHAIN (go1.26.4), not from memory: syscall/exec_windows.go
// declares SysProcAttr with HideWindow, CmdLine, CreationFlags, Token,
// ProcessAttributes, ThreadAttributes, NoInheritHandles,
// AdditionalInheritedHandles and ParentProcess — no Setpgid, no Setsid, no
// Pdeathsig. A shared untagged file naming Setpgid does not merely behave
// differently here; it fails to compile, which is how the windows client
// release build broke.
//
// CREATE_NEW_PROCESS_GROUP IS THE HONEST ANALOG OF SETPGID, and only of
// Setpgid. syscall/types_windows.go defines it as 0x00000200; it puts the child
// in its own console process group, so a CTRL_C_EVENT or CTRL_BREAK_EVENT
// delivered to this process's group does not also reach the child. That is the
// same property the unix arm buys: the signal aimed at the parent's group stops
// at the parent.
//
// WHAT WINDOWS DOES NOT HAVE, stated rather than papered over:
//   - No launchctl-style group kill to survive. The unix arm exists because
//     `launchctl bootout` signals the whole process group; the Windows SCM
//     stopping a service does not walk children, so the child of a stopping
//     service is not killed by that stop in the first place.
//   - No cgroup to escape, so HALF TWO (the systemd transient scope) has no
//     counterpart and none is faked. A Job Object is the nearest Windows
//     construct, and it is the OPPOSITE tool — it would bind the child to the
//     parent's lifetime rather than free it from it.
//
// THIS ARM HAS NOT BEEN EXECUTED, the same disclosure the other windows arms in
// this tree carry (segmentdist/dirsync_windows.go, mapfile_windows.go): there is
// no Windows machine in the loop, and CI builds the windows/amd64 client but
// runs no tests on it.
func handoffSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
