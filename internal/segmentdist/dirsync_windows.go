// SPDX-License-Identifier: Apache-2.0

//go:build windows

package segmentdist

// fsyncDir does nothing on this platform, and the reason is the platform's own
// semantics rather than a shortcut taken here.
//
// READ FROM THE PINNED TOOLCHAIN (go1.26.4), not from memory: os.File.Sync calls
// poll.FD.Fsync calls syscall.Fsync, and syscall/syscall_windows.go defines Fsync on
// this platform as FlushFileBuffers. The same file's Open shows that a directory handle
// is obtainable only through the read arm — FILE_FLAG_BACKUP_SEMANTICS is added when
// the access flags are not O_WRONLY/O_RDWR, and the write arm is deliberately left to
// fail with ERROR_ACCESS_DENIED, which Open maps to EISDIR. RELAYED from Microsoft's
// published contract, not executed here: FlushFileBuffers requires the handle to carry
// GENERIC_WRITE. Taken together, os.Open(dir).Sync() on Windows is not a weaker flush;
// it is an error on every single write, which would convert a durability fix into a
// total write outage on the platform.
//
// SO THIS IS NOT A SWALLOWED FAILURE. There is no directory-flush operation available
// to attempt, hence nothing whose failure could be hidden — the unix arm returns its
// errors precisely because it has one to run. What IS true, and is stated rather than
// papered over: the crash window the unix arm closes stays open on Windows, and no code
// in this package can close it.
//
// LIKE mapfile_windows.go, THIS ARM HAS NOT BEEN EXECUTED — there is no Windows machine
// in the loop. CI builds the windows/amd64 client but runs no tests on it.
func fsyncDir(string) error { return nil }
