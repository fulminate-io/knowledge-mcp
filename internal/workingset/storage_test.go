// SPDX-License-Identifier: Apache-2.0

package workingset

import (
	"testing"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtypes"
)

func TestStorageAdmissionsRemainIndependent(t *testing.T) {
	s := New()
	local := Ref{GraphType: kgtypes.GraphCode, Name: "same", Storage: "local"}
	cloud := Ref{GraphType: kgtypes.GraphCode, Name: "same", Storage: "cloud", Account: "a"}
	if !s.AdmitRef(local, "collect") || !s.AdmitRef(cloud, "collect") {
		t.Fatal("duplicate storage copy was not admitted")
	}
	if len(s.Members()) != 2 {
		t.Fatal("storage identities collapsed")
	}
	if !s.RemoveRef(local) || !s.HasRef(cloud) || s.HasRef(local) {
		t.Fatal("removal crossed storage identity")
	}
}
