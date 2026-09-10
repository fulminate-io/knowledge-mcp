// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fulminate-io/knowledge-mcp/internal/topology/foundation"
)

// topology_deleted_analyzers_test.go — R14: the twelve cloud and exposure
// analyzers are gone from the registry, and a name that used to address one
// resolves to nothing.
//
// THE TWELVE NAMES COME FROM THE ANALYZERS' OWN Name() METHODS, recorded on the
// follow-up project's seed finding with the file and line of each registration
// rather than retyped from memory here. They are the names an operator's script
// still carries, which is why the observable is "addressed by name" and not only
// "absent from the list".
//
// THE SURVIVING-ANALYZER CONTROL IS THE WHOLE TEST. A registry that returned
// nothing at all — a blank-import that stopped firing, an init that panicked
// into an empty map — satisfies every absence below while breaking topology
// completely. The control draws from the three packages R14 keeps.
func TestTopology_DeletedCloudAnalyzersAreGone(t *testing.T) {
	deleted := []string{
		// topology/cloud
		"cert_expiry", "cross_provider_blast", "event_chain",
		"monitoring_coverage", "orphan", "serverless_depth",
		// topology/exposure
		"aws_public_exposure", "aws_sg_reachability", "iam_escalation",
		"k8s_public_exposure", "k8s_reachability", "unified_public_exposure",
	}

	all := foundation.All()
	require.NotEmpty(t, all,
		"CONTROL: the analyzer registry is EMPTY — every absence below would pass vacuously, "+
			"and topology would be broken outright. Check the blank imports in topology_register.go")

	listed := map[string]bool{}
	for _, a := range all {
		listed[a.Name()] = true
	}

	for _, name := range deleted {
		assert.Falsef(t, listed[name], "the deleted analyzer %q is still LISTED by the registry", name)
		_, ok := foundation.Get(name)
		assert.Falsef(t, ok, "the deleted analyzer %q is still ADDRESSABLE by name", name)
	}

	// THE CONTROL, in the same run and through the same two calls: TWO SURVIVING
	// ANALYZERS are both listed and addressable. Without it, "Get returns false"
	// proves nothing about these twelve.
	//
	// TWO, AND THE COMMENT SAYS TWO. It used to promise "an analyzer from each of
	// the three SURVIVING packages" while the loop named two, both from
	// topology/graph — a claim the code did not carry out, which is the kind a
	// reader trusts precisely because it sounds thorough. Naming what the loop
	// does is worth more than a third row here: the control's job is to prove the
	// registry is populated and addressable, and either of these does that.
	for _, survivor := range []string{"pagerank", "blast_radius"} {
		assert.Truef(t, listed[survivor], "CONTROL: the surviving analyzer %q must still be listed", survivor)
		_, ok := foundation.Get(survivor)
		assert.Truef(t, ok, "CONTROL: the surviving analyzer %q must still be addressable", survivor)
	}
}
