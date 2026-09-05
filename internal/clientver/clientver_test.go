// SPDX-License-Identifier: Apache-2.0

package clientver

import (
	"net/http"
	"runtime"
	"testing"
)

func TestStamp_SetsBothIdentityHeaders(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "1.2.3"

	h := make(http.Header)
	Stamp(h)

	if got := h.Get(HeaderVersion); got != "1.2.3" {
		t.Fatalf("%s = %q, want %q", HeaderVersion, got, "1.2.3")
	}
	want := runtime.GOOS + "-" + runtime.GOARCH
	if got := h.Get(HeaderPlatform); got != want {
		t.Fatalf("%s = %q, want %q", HeaderPlatform, got, want)
	}
}

func TestStamp_ReplacesAStaleValueRatherThanAppending(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "9.9.9"

	h := make(http.Header)
	h.Set(HeaderVersion, "0.0.1")
	Stamp(h)

	if vals := h.Values(HeaderVersion); len(vals) != 1 || vals[0] != "9.9.9" {
		t.Fatalf("%s values = %#v, want exactly [\"9.9.9\"]", HeaderVersion, vals)
	}
}

func TestHeaderSpellings(t *testing.T) {
	if HeaderVersion != "X-Knowledge-Client-Version" {
		t.Fatalf("HeaderVersion = %q", HeaderVersion)
	}
	if HeaderPlatform != "X-Knowledge-Client-Platform" {
		t.Fatalf("HeaderPlatform = %q", HeaderPlatform)
	}
}
