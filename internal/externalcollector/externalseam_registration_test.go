// SPDX-License-Identifier: Apache-2.0

package externalcollector

import (
	"maps"
	"os"
	"testing"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// externalseam_registration_test.go — WHERE THE SEAM IS APPLIED: the one
// construction point every stdio registration in this package goes through, and
// the command-and-skip decision it makes. Split from externalseam_test.go, which
// keeps the seam's DECLARATIONS — the variables, the two test lists, the mode
// vocabulary and their parsing. No assertion moved with the split.

// stdioDefFor is stdioDefEnv with the calling test's name supplied explicitly,
// and it is the seam's construction point.
//
// THE NAME IS A PARAMETER because it is what the seam scopes on: the dialing
// tests dial the external command when the seam is on, and the six that share
// this construction point keep the re-execed Go stub. Passing the name lets the
// scoping test build the registration each of them would get and OBSERVE the
// command, rather than infer the scope from which tests happened to pass.
//
// IT LIVES HERE RATHER THAN BESIDE stdioDefEnv because it is the seam's, and
// because stubprovider_test.go crossed this repository's 500-line hard cap when
// the describe contract landed: the file-length gate refused a commit carrying
// it, and moving the seam's own function is the split that keeps each file about
// one subject.
func stdioDefFor(t *testing.T, testName, mode string, env map[string]string) *Registration {
	t.Helper()
	command, args, external := externalStdioCommand(t, testName, mode)
	if !external {
		self, err := os.Executable()
		if err != nil {
			t.Fatalf("resolving the test binary: %v", err)
		}
		// The marker is what lets a child that did not receive its mode exit
		// instead of running the suite. See TestMain. It is the Go stub's own
		// switch and is never passed to an external collector.
		command, args = self, []string{stubArgvMarker}
	}
	block := map[string]string{stubModeEnv: mode}
	maps.Copy(block, env)
	return &Registration{Def: &knowledgev1.GraphTypeDef{
		Name: stubFamily,
		Collector: &knowledgev1.CollectorSpec{
			Tool: defaultStubTool,
			Provider: &knowledgev1.CollectorSpec_Stdio{Stdio: &knowledgev1.StdioProvider{
				Command: command,
				Args:    args,
				Env:     sortedEnvPairs(block),
			}},
		},
	}}
}

// externalStdioCommand returns the command and arguments the stdio registration
// for `testName` in `mode` must carry, and skips the calling test when the mode
// is one the external collector does not emulate.
//
// THE SKIP'S GRANULARITY IS THE CALLER'S OWN t: called from a subtest body it
// ends that subtest and leaves its siblings running; called from a top-level
// body it ends the whole test. That is the existing structure of these tests and
// not a choice this seam makes.
func externalStdioCommand(t *testing.T, testName, mode string) (string, []string, bool) {
	t.Helper()
	command, args, set, err := externalCollector()
	if err != nil {
		t.Fatalf("reading the external collector seam: %v", err)
	}
	if !set || !dialsExternalCollector(testName) {
		return "", nil, false
	}
	skip, err := unemulatedModes()
	if err != nil {
		t.Fatalf("reading the external collector seam: %v", err)
	}
	if skip[mode] {
		t.Skipf("stub mode %q is not emulated by the external collector %q", mode, command)
	}
	return command, args, true
}
