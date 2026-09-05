// SPDX-License-Identifier: Apache-2.0

// Package clientver holds the client's build identity — the version string it
// claims on the wire, the platform it runs on, and the one stamping function
// that puts both on a cloud-bound request.
//
// Why a leaf package rather than reusing bootstrap.Version: the version is
// declared in cmd/knowledge/internal/bootstrap, and bootstrap IMPORTS
// cmd/knowledge/internal/auth while auth imports bootstrap nowhere. auth is one
// of the two transports that must stamp the header, so it cannot reach
// bootstrap.Version without an import cycle. clientver imports only net/http,
// os, runtime and the hashing stdlib, so both transports can depend on it.
package clientver

import (
	"net/http"
	"runtime"
)

// Version is the client's build version, the same value cmd/knowledge/main.go
// publishes into bootstrap.Version from its ldflags-injected `version`. The
// default matches bootstrap's: an unstamped local build claims "dev".
//
// This is the identity the client CLAIMS. The possession proof in this package
// is what binds that claim to the bytes actually running, so the two must never
// be published independently — main.go's init assigns both from one source and
// a test in package main guards against a future edit that publishes only one.
var Version = "dev"

// platform is resolved once at init rather than formatted per request: it is a
// constant for the life of the process and it is read on every cloud-bound
// request.
var platform = runtime.GOOS + "-" + runtime.GOARCH

// Platform returns the GOOS-GOARCH pair this binary is running on, e.g.
// "darwin-arm64".
//
// It reports what the binary IS and never validates that value against a
// release matrix. A platform with no published release artifact — darwin-amd64,
// or any build-from-source target — therefore reports honestly and is refused
// by the gateway, which is the deliberate posture: an honest unprovable claim
// is preferable to a flattering one. Note also that the pair does not uniquely
// identify a binary on linux (a static and a standard build both report
// linux-amd64); resolving that is the verifier's problem, not the client's.
func Platform() string { return platform }

// HeaderVersion is the request header carrying [Version].
const HeaderVersion = "X-Knowledge-Client-Version"

// HeaderPlatform is the request header carrying [Platform].
const HeaderPlatform = "X-Knowledge-Client-Platform"

// Stamp sets both client-identity headers on h.
//
// One implementation on purpose: the client has two cloud-bound transports, and
// a second hand-rolled stamping site is how they come to disagree about the
// spelling or the value. Both transports call this.
func Stamp(h http.Header) {
	h.Set(HeaderVersion, Version)
	h.Set(HeaderPlatform, Platform())
}
