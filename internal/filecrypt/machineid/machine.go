// SPDX-License-Identifier: Apache-2.0

// Package machineid resolves a stable per-host identifier and derives one of
// the per-build key fragments from it. The identifier is what binds the
// client's at-rest caches to this installation: files sealed on one machine do
// not open on another.
//
// PROVENANCE: transcribed verbatim from the server package
// cmd/knowledge-server/internal/store/machineid. It is copied rather than
// imported because that package sits under an internal/ directory rooted at
// the server command, so the import is refused at compile time with "use of
// internal package ... not allowed", and because the two commands are separate
// modules. Resolving it with a hand-written package shared by both binaries is
// denied by this repo's architecture invariant, which admits only generated
// protobuf as a cross-module contract. Every function body below is
// byte-identical to that original; only this documentation differs.
//
// Both binaries read and write the same ~/.knowledge/machine-id cache file and
// therefore converge on the same value on a host where both run. They do not
// need to: this key protects only files this binary writes and reads, so a
// divergence costs one cold rebuild of a rebuildable cache and nothing else.
package machineid

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// MachineID returns a stable identifier for this machine that survives
// reboots, network configuration changes, and interface enumeration
// shuffles. The returned value is always 16 lowercase hex chars.
//
// Resolution order (first non-empty wins):
//
//  1. Cached value at ~/.knowledge/machine-id. Once written this is the
//     canonical answer — even if a later platform-source call returns
//     something different (e.g., after a hardware repair that regenerates
//     IOPlatformUUID), the cache wins. This is the load-bearing property
//     for "stable across reboots." The cache is the source of truth that
//     binds an encrypted file to "this installation."
//
//  2. Platform-stable identifier:
//     - macOS:   IOPlatformUUID via `ioreg -c IOPlatformExpertDevice`.
//     - Linux:   /etc/machine-id (with /var/lib/dbus/machine-id fallback).
//     - Windows: not implemented — falls through to (3).
//
//  3. Fallback: hostname + first PHYSICAL non-loopback interface MAC,
//     where "physical" means the name does NOT match any synthetic
//     prefix (anpi*, awdl*, llw*, utun*, bridge*, gif*, stf*, ap[0-9]*).
//     Remaining interfaces are sorted alphabetically so the picked MAC is
//     stable even if net.Interfaces enumeration order varies.
//
// On first successful resolve via (2) or (3), the result is hashed with
// SHA-256 and the first 16 hex chars are written to the cache file. This
// makes subsequent calls O(1) and ensures the value never changes once
// established — a host that reinstalls macOS but keeps ~/.knowledge keeps
// the same MachineID and decrypts existing files without recovery work.
//
// HISTORICAL NOTE: a prior implementation hashed `hostname + firstMAC`
// directly without filtering synthetic interfaces. macOS's anpi0/anpi1/
// anpi2 (Apple Network Private Interface) endpoints have rotating
// MACs across boots, so the picked MAC — and therefore the derived
// MachineID — was not stable. Encrypted graphs failed to decrypt with
// "cipher: message authentication failed" after any boot that shuffled
// interface order. The cache + filtering above eliminates both failure
// modes. Recovery for graphs encrypted under the old broken function
// requires a one-shot re-encrypt with the OLD hash known.
func MachineID() string {
	cachedOnce.Do(func() {
		cachedID = resolveAndCache()
	})
	return cachedID
}

var (
	cachedOnce sync.Once
	cachedID   string
)

// resolveAndCache walks the resolution chain and returns a 16-char hex ID.
// On the first successful resolve via platform or fallback, persists to
// the cache file under ~/.knowledge/machine-id so subsequent process
// starts read the cache first.
func resolveAndCache() string {
	if id := readCache(); id != "" {
		return id
	}
	raw := resolvePlatform()
	if raw == "" {
		raw = resolveFallback()
	}
	h := sha256.New()
	h.Write([]byte(raw))
	id := hex.EncodeToString(h.Sum(nil))[:16]
	_ = writeCache(id) // best-effort; cache is a stability optimization, not a hard requirement
	return id
}

// cachePath returns the absolute path to the machine-id cache file.
// Empty string when the user's home directory cannot be resolved
// (extremely unusual; signals a broken environment).
func cachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".knowledge", "machine-id")
}

// readCache returns the cached MachineID or empty string when the cache
// is absent / unreadable / malformed (anything other than a 16-char
// hex string after whitespace trim).
func readCache() string {
	p := cachePath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if len(s) != 16 {
		return ""
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return strings.ToLower(s)
}

// writeCache persists the MachineID to ~/.knowledge/machine-id with
// 0o600 perms. Creates the parent dir with 0o700 if missing.
// Best-effort — caller treats failure as "we'll re-derive next time."
func writeCache(id string) error {
	p := cachePath()
	if p == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(id+"\n"), 0o600)
}

// resolvePlatform dispatches to the platform-specific stable-ID source.
// Returns empty string on unsupported platforms or lookup failure;
// caller falls through to resolveFallback.
func resolvePlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return resolveMacOS()
	case "linux":
		return resolveLinux()
	default:
		return ""
	}
}

// resolveMacOS reads IOPlatformUUID via ioreg. Output shape:
//
//	|   "IOPlatformUUID" = "857F2365-6DD3-556B-B456-BA0EEAC69A9F"
//
// We extract the value between the second pair of quotes. ioreg is in
// /usr/sbin on every macOS install since 10.0 — the exec call cannot
// fail unless the user has actively broken their system PATH.
func resolveMacOS() string {
	out, err := exec.Command("ioreg", "-d2", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if !strings.Contains(line, "IOPlatformUUID") {
			continue
		}
		parts := strings.Split(line, "\"")
		if len(parts) >= 4 {
			return parts[3]
		}
	}
	return ""
}

// resolveLinux reads /etc/machine-id (the systemd canonical location)
// with /var/lib/dbus/machine-id as a fallback for older / non-systemd
// distros. Both are typically 32 lowercase hex chars.
func resolveLinux() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(b))
		if s != "" {
			return s
		}
	}
	return ""
}

// syntheticPrefixes are interface name prefixes whose MAC addresses are
// not stable across reboots OR whose enumeration order is not stable.
// Any interface matching one of these prefixes is skipped during
// resolveFallback's interface scan.
//
//   - anpi*  Apple Network Private Interface (CoreNetworking synthetic)
//   - awdl*  Apple Wireless Direct Link
//   - llw*   Low-Latency WLAN
//   - utun*  Userspace tunnel (VPN)
//   - bridge*/gif*/stf* Virtual / tunnel
//   - ap*    Soft access point (the dynamic ap0/ap1 macOS exposes)
var syntheticPrefixes = []string{
	"anpi", "awdl", "llw", "utun", "bridge", "gif", "stf", "ap",
}

// resolveFallback derives an identifier from hostname + the first stable
// physical interface MAC. Synthetics are filtered out; remaining
// interfaces are sorted alphabetically so the chosen one is stable
// regardless of the OS's internal enumeration order.
//
// When no physical interface is present, returns the hostname only —
// the caller hashes whatever we return into 16 hex chars, so any stable
// non-empty input produces a stable MachineID.
func resolveFallback() string {
	hostname, _ := os.Hostname()
	ifaces, _ := net.Interfaces()
	var stable []net.Interface
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		if isSynthetic(iface.Name) {
			continue
		}
		stable = append(stable, iface)
	}
	sort.Slice(stable, func(i, j int) bool { return stable[i].Name < stable[j].Name })
	if len(stable) == 0 {
		return hostname
	}
	return hostname + ":" + stable[0].HardwareAddr.String()
}

// isSynthetic reports whether name starts with any of syntheticPrefixes.
func isSynthetic(name string) bool {
	for _, p := range syntheticPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
