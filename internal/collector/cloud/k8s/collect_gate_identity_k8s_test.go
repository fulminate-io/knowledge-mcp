// SPDX-License-Identifier: Apache-2.0

package k8s

// collect_gate_identity_k8s_test.go pins the COLLECTOR HALF of the k8s gate
// identity — the half the collect dispatch's derivation cannot assert about
// itself.
//
// The dispatch derives a k8s collect's graph name as the collect id VERBATIM.
// That is only correct if the collector, handed the same id, really does name
// its graph after it: the collector's GraphName is bundle.contextName, which
// buildRestConfig resolves. So this test asks buildRestConfig what it resolves,
// with an external expectation the test does not supply the answer to.
//
// IT IS K8S AND NOT GCP OR AZURE because k8s is the one of the three whose
// collector half runs through a RESOLUTION function rather than a straight
// assignment: gcp's resolveProject returns a non-empty id unchanged on a
// one-line branch and azure assigns subscriptionID := id, both settled by
// reading them, while buildRestConfig loads and MERGES kubeconfig files and
// could plausibly rewrite what it was handed.
//
// IT DIALS NOTHING. buildRestConfig only loads and merges kubeconfig and
// constructs a *rest.Config; no client is built and no request is made, so the
// unroutable server address in the fixture below is never contacted.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// gateIdentityKubeconfig is an inline kubeconfig carrying TWO contexts, so the
// requested one and the file's own current-context are distinguishable. The
// server is an unroutable loopback port and the token is a placeholder; nothing
// here is dialed.
const gateIdentityKubeconfig = `apiVersion: v1
kind: Config
current-context: ctx-from-the-file
clusters:
- name: only-cluster
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: ctx-from-the-file
  context:
    cluster: only-cluster
    user: only-user
- name: ctx-from-the-request
  context:
    cluster: only-cluster
    user: only-user
users:
- name: only-user
  user:
    token: placeholder-not-a-credential
`

// TestBuildRestConfig_ResolvesTheRequestedContextVerbatim proves a non-empty
// collect id is the resolved context name VERBATIM, which is what the k8s
// collector then uses as its graph name and what the collect dispatch derives
// the gate identity from.
func TestBuildRestConfig_ResolvesTheRequestedContextVerbatim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(gateIdentityKubeconfig), 0o600))
	// t.Setenv restores the environment afterwards and forbids parallel
	// execution, which is what keeps this from leaking into sibling tests.
	t.Setenv("KUBECONFIG", path)

	// buildRestConfig returns (*rest.Config, string, error) — the resolved
	// context name is the SECOND value.
	_, resolved, err := buildRestConfig("ctx-from-the-request")
	require.NoError(t, err, "a kubeconfig naming the requested context must resolve")
	require.Equal(t, "ctx-from-the-request", resolved,
		"a non-empty collect id is the resolved context name verbatim, and that name "+
			"is what the collector uses as its graph name — so the dispatch deriving the "+
			"id verbatim is exact only while this holds")

	// DISCRIMINATING CONTROL. Without it, a function that merely echoed its
	// argument would satisfy the assertion above. NAME THE CATCHER: this is what
	// proves buildRestConfig RESOLVES rather than echoes. It also documents why
	// an empty id has no pre-walk derivation: the answer comes from the file, not
	// from the request.
	_, fromFile, err := buildRestConfig("")
	require.NoError(t, err, "a kubeconfig with a current-context must resolve with no request")
	require.Equal(t, "ctx-from-the-file", fromFile,
		"with no requested context the resolution reads the kubeconfig's own "+
			"current-context, which is why an empty collect id cannot be predicted "+
			"from the request")
}
