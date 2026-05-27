// SPDX-License-Identifier: Apache-2.0

package exposure

import (
	"testing"
)

// public_exposure_registration_test.go pins the three public-exposure
// analyzers into topology.All() so future patches that accidentally
// delete their init() blocks (or break a sibling file that happens to
// short-circuit the package load) fail CI immediately.

func TestPublicExposureAnalyzers_Registered(t *testing.T) {
	want := map[string]bool{
		"aws_public_exposure":     false,
		"k8s_public_exposure":     false,
		"unified_public_exposure": false,
	}
	for _, a := range All() {
		if _, ok := want[a.Name()]; ok {
			want[a.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("topology.All() missing %q", name)
		}
	}
}

// TestPublicExposureAnalyzers_LookupByName verifies each analyzer is
// resolvable via topology.Get by its stable identifier.
func TestPublicExposureAnalyzers_LookupByName(t *testing.T) {
	for _, name := range []string{
		"aws_public_exposure",
		"k8s_public_exposure",
		"unified_public_exposure",
	} {
		a, ok := Get(name)
		if !ok {
			t.Errorf("topology.Get(%q) = _, false", name)
			continue
		}
		if a.Name() != name {
			t.Errorf("topology.Get(%q).Name() = %q", name, a.Name())
		}
	}
}
