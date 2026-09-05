// SPDX-License-Identifier: Apache-2.0

//go:build unix

package bootstrap

import "syscall"

// handoffSysProcAttr returns the process attributes the restart handoff child
// is spawned with on unix.
//
// HALF ONE OF THE SURVIVAL FIX, and it is MEASURED rather than reasoned. A
// child forked the ordinary way shares the parent's process group, and
// `launchctl bootout` — the exact call the unit stop makes — kills such a
// child: a plain child's heartbeat froze at the bootout while a child spawned
// with this attribute kept advancing through the identical call.
//
// SETPGID IS NOT SETSID. This creates a process GROUP, not a session, so the
// macOS session-leader caveat that governs Setsid is untouched here. Said
// explicitly because the next reader will otherwise see a SysProcAttr and
// conclude that caveat was ignored.
//
// HALF TWO, the cgroup escape that linux/systemd needs, is NOT here: Setpgid
// does not escape a cgroup, so that half is carried by the transient-scope
// launcher in the child's argv (client_update_handoff_argv.go), not by these
// attributes.
func handoffSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
