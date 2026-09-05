// SPDX-License-Identifier: Apache-2.0

package graphclient

import (
	"strings"
	"testing"
)

// TestServerBinarySkewLine tables the client-versus-installed-server-binary
// skew helper.
//
// The EQUAL-stamps rows are the discriminating controls: a helper hard-wired to
// report skew would pass every differing row on its own.
func TestServerBinarySkewLine(t *testing.T) {
	cases := []struct {
		name       string
		clientVer  string
		serverBin  string
		wantSkewed bool
	}{
		{name: "equal release stamps are quiet", clientVer: "v0.4.10", serverBin: "v0.4.10", wantSkewed: false},
		{name: "equal dev stamps are quiet", clientVer: "dev", serverBin: "dev", wantSkewed: false},
		{name: "differing release stamps skew", clientVer: "v0.4.11", serverBin: "v0.4.10", wantSkewed: true},
		{name: "a dev client against a release server binary skews", clientVer: "dev", serverBin: "v0.4.10", wantSkewed: true},
		{name: "a release client against a dev server binary skews", clientVer: "v0.4.10", serverBin: "dev", wantSkewed: true},
		{name: "an empty server binary version is unknown, never a skew", clientVer: "v0.4.10", serverBin: "", wantSkewed: false},
		{name: "an empty client version is unknown, never a skew", clientVer: "", serverBin: "v0.4.10", wantSkewed: false},
		{name: "both empty is unknown", clientVer: "", serverBin: "", wantSkewed: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line, skewed := ServerBinarySkewLine(tc.clientVer, tc.serverBin)
			if skewed != tc.wantSkewed {
				t.Fatalf("skewed = %v, want %v (client %q, server binary %q)", skewed, tc.wantSkewed, tc.clientVer, tc.serverBin)
			}
			if !skewed {
				if line != "" {
					t.Errorf("a non-skewed pair must render nothing, got %q", line)
				}
				return
			}
			// When it fires the line must name BOTH stamps — an operator who
			// cannot see which two versions disagree cannot act on it.
			for _, want := range []string{tc.clientVer, tc.serverBin} {
				if !strings.Contains(line, want) {
					t.Errorf("the skew line omits %q: %q", want, line)
				}
			}
			// And it must name the remedy for THIS divergence, which is not the
			// daemon restart the sibling line prescribes: a binary on disk from
			// a different release is not repaired by restarting anything.
			if !strings.Contains(line, "knowledge install") {
				t.Errorf("the line must name the re-install remedy, not a restart: %q", line)
			}
			if strings.Contains(line, "restart the daemon") {
				t.Errorf("this divergence is NOT fixed by a daemon restart; the line must not say so: %q", line)
			}
		})
	}

	// The two helpers must stay distinguishable in the rendered output, or a
	// surface carrying both would read as one repeated line.
	daemonLine, _ := VersionSkewLine("v1.0.0", "v0.9.0")
	binaryLine, _ := ServerBinarySkewLine("v1.0.0", "v0.9.0")
	if daemonLine == binaryLine {
		t.Errorf("the daemon-skew and binary-skew lines are identical; they describe different divergences with different remedies")
	}
}
