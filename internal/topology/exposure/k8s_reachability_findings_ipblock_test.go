// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// k8s_reachability_findings_ipblock_test.go covers classifyCIDR and
// findWorldExposedPods: the per-CIDR severity table and the end-to-end
// NetworkPolicy JSON re-parse path.

// TestClassifyCIDR_Table walks every severity branch classifyCIDR supports
// and asserts the returned Severity. The table intentionally exercises
// both boundary masks and representative CIDRs.
func TestClassifyCIDR_Table(t *testing.T) {
	cases := []struct {
		name string
		cidr string
		want Severity
	}{
		// 0.0.0.0/0 is the literal internet — critical.
		{"world_open", "0.0.0.0/0", SeverityCritical},

		// Public /1..15 → critical (plan's "high" tier mapped to critical).
		{"public_slash_1", "0.0.0.0/1", SeverityCritical},
		{"public_slash_8", "8.0.0.0/8", SeverityCritical},
		{"public_slash_15", "8.0.0.0/15", SeverityCritical},

		// Public /16..23 → warning.
		{"public_slash_16", "8.8.0.0/16", SeverityWarning},
		{"public_slash_23", "8.8.0.0/23", SeverityWarning},

		// Public /24 and narrower → notice.
		{"public_slash_24", "8.8.8.0/24", SeverityNotice},
		{"public_slash_32", "8.8.8.8/32", SeverityNotice},

		// RFC1918 → info regardless of mask size.
		{"rfc1918_10", "10.0.0.0/8", SeverityInfo},
		{"rfc1918_10_narrow", "10.1.2.0/24", SeverityInfo},
		{"rfc1918_172", "172.16.0.0/12", SeverityInfo},
		{"rfc1918_192", "192.168.0.0/16", SeverityInfo},

		// Link-local and loopback → suppressed (empty Severity).
		{"loopback", "127.0.0.0/8", ""},
		{"loopback_narrow", "127.0.0.1/32", ""},
		{"link_local", "169.254.0.0/16", ""},
		{"link_local_narrow", "169.254.1.0/24", ""},

		// Garbage → suppressed so bad inputs never trip a finding.
		{"garbage", "not-a-cidr", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyCIDR(tc.cidr)
			assert.Equal(t, tc.want, got, "cidr=%q", tc.cidr)
		})
	}
}

// TestClassifyCIDR_Under40Lines is a belt-and-braces check that the
// classifyCIDR helper stays under 40 lines (the Phase 4 Step 3 criterion).
// Reads the source file and counts lines between the function signature
// and its closing brace.
func TestClassifyCIDR_Under40Lines(t *testing.T) {
	src := mustReadFile(t, "k8s_reachability_findings_ipblock.go")
	idx := indexOf(src, "\nfunc classifyCIDR")
	require.GreaterOrEqual(t, idx, 0, "classifyCIDR must be defined")
	// Advance past the signature and count until the matching closing brace
	// at column 0.
	depth := 0
	lines := 0
	started := false
	for i := idx + 1; i < len(src); i++ {
		if src[i] == '{' {
			depth++
			started = true
		}
		if src[i] == '\n' && started {
			lines++
		}
		if src[i] == '}' {
			depth--
			if depth == 0 && started {
				break
			}
		}
	}
	assert.LessOrEqual(t, lines, 40, "classifyCIDR must be <= 40 lines")
}

// TestClassifyCIDR_UsesNetParseCIDR pins that classifyCIDR calls
// net.ParseCIDR (the plan criterion). Grepping source is cheap and
// catches accidental rewrites to a regex-based implementation.
func TestClassifyCIDR_UsesNetParseCIDR(t *testing.T) {
	src := mustReadFile(t, "k8s_reachability_findings_ipblock.go")
	assert.Contains(t, src, `net.ParseCIDR`)
}

// buildIPBlockFixture creates a NetworkPolicy whose ingress rules include a
// single ipBlock.cidr entry. The CIDR text is caller-provided so one helper
// drives every fixture variant.
func buildIPBlockFixture(t *testing.T, cidr string) *cloudFixture {
	t.Helper()
	fx := newCloudFixture(t)
	addPod(fx, "default/Pod/target", "default")
	policyID := addNetworkPolicy(fx, "default/NetworkPolicy/public-ingress", "default")
	// Set the NetworkPolicy Content to a JSON document the classifier
	// re-parses.  Only the spec.ingress[].from[].ipBlock.cidr path is read.
	content := `{
  "spec": {
    "ingress": [
      {
        "from": [
          { "ipBlock": { "cidr": "` + cidr + `" } }
        ]
      }
    ]
  }
}`
	fx.setNodeContent(k8sReachabilityAcct, policyID, content)
	return fx
}

// TestFindWorldExposedPods_FiresFor0000 asserts a NetworkPolicy with a
// 0.0.0.0/0 ipBlock produces a world-exposure finding.
func TestFindWorldExposedPods_FiresFor0000(t *testing.T) {
	fx := buildIPBlockFixture(t, "0.0.0.0/0")
	findings := runClassify(t, fx, nil)

	var worldExposed []Finding
	for _, f := range findings {
		if indexOf(f.Title, "admits ingress from 0.0.0.0/0") >= 0 {
			worldExposed = append(worldExposed, f)
		}
	}
	require.Len(t, worldExposed, 1)
	assert.Equal(t, SeverityCritical, worldExposed[0].Severity)
	assert.Equal(t, "0.0.0.0/0", worldExposed[0].Metadata["cidr"])
}

// TestFindWorldExposedPods_IgnoresRFC1918 asserts a NetworkPolicy allowing
// only RFC1918 produces no world-exposure finding above info — the only
// emitted finding would be info severity which is an acceptable report but
// the classifier MUST NOT fire a warning-or-higher finding for 10.0.0.0/8.
func TestFindWorldExposedPods_IgnoresRFC1918(t *testing.T) {
	fx := buildIPBlockFixture(t, "10.0.0.0/8")
	findings := runClassify(t, fx, nil)

	// Fires at info severity (not suppressed) but never at warning+.
	for _, f := range findings {
		if indexOf(f.Title, "admits ingress from") < 0 {
			continue
		}
		assert.NotEqual(t, SeverityCritical, f.Severity)
		assert.NotEqual(t, SeverityWarning, f.Severity)
	}
}
