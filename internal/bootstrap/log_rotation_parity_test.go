// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

// THE FOUR ROTATION FLAGS ARE THE SAME IN BOTH BINARIES, and that parity has to
// be anchored at BOTH ENDS or it is not pinned at all.
//
// The two modules cannot import each other, so the retention policy is mirrored
// rather than shared, and the thing held identical is the operator-facing
// surface: the flag names, the defaults and the meaning of zero. An earlier
// version of this test compared the client's flagset against four literals typed
// into the test body, which could only ever catch the CLIENT drifting from those
// literals — changing the SERVER's registered defaults to 99 and 7 left it green.
//
// SO THERE ARE TWO ASSERTIONS, and each binary carries its own copy:
//  1. this binary's OWN four defaults, read from its REAL registration;
//  2. the SIBLING's four, read from the sibling's registration source, compared
//     against this binary's registered values.
//
// A drift at either end reds in both modules.
//
// THE TWO ASSERTIONS LIVE IN TWO FILES, and the split is a publication boundary
// rather than a style choice. Assertion 1 is about THIS module and travels with
// it; assertion 2 reads knowledge-server's source and its subject is a tree the
// public mirror does not contain, so it sits in
// log_rotation_parity_sibling_test.go, which scripts/sync-to-oss.sh removes from
// the published tree by name. Both run here, on every staging test run.

// rotationFlagNames are the four keys whose names, defaults and meaning of zero
// the two binaries hold identical. Read by both halves.
var rotationFlagNames = []string{
	"log-rotate-max-size-mb",
	"log-rotate-max-files",
	"log-rotate-max-age-days",
	"log-rotate-compress",
}

// TestRotationFlagDefaultsAreRegisteredHere pins THIS binary's own four, read
// from the real registration rather than from a value the test also supplies.
func TestRotationFlagDefaultsAreRegisteredHere(t *testing.T) {
	fs := flag.NewFlagSet("knowledge serve", flag.ContinueOnError)
	var cfg Config
	registerConfigFlags(fs, &cfg)

	want := map[string]string{
		"log-rotate-max-size-mb":  "50",
		"log-rotate-max-files":    "3",
		"log-rotate-max-age-days": "30",
		"log-rotate-compress":     "true",
	}
	for _, name := range rotationFlagNames {
		f := fs.Lookup(name)
		require.NotNil(t, f, "--%s must be registered", name)
		require.Equal(t, want[name], f.DefValue, "--%s default", name)
	}

	// CONTROL: DefValue reports a real registered default through this same
	// instrument, so the four readings above are readings and not constants the
	// check would print for any flag.
	require.Equal(t, "info", fs.Lookup("log-level").DefValue)
}
