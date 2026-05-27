// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"testing"
)

func TestNormalizeIssuer(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://gitlab.com/", "https://gitlab.com"},
		{"https://gitlab.com", "https://gitlab.com"},
		{"https://gitlab.example.com///", "https://gitlab.example.com"},
	}

	for _, tt := range tests {
		got := normalizeIssuer(tt.input)
		if got != tt.want {
			t.Errorf("normalizeIssuer(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsOIDCRelevantType(t *testing.T) {
	relevant := []string{
		"iam-role", "iam-openid-connect-provider",
		"managed-identity", "federated-credential",
		"workload-identity-pool", "workload-identity-provider",
	}
	for _, rt := range relevant {
		if !isOIDCRelevantType(rt) {
			t.Errorf("expected %q to be OIDC relevant", rt)
		}
	}

	irrelevant := []string{"ec2-instance", "s3-bucket", "service", "deployment", ""}
	for _, rt := range irrelevant {
		if isOIDCRelevantType(rt) {
			t.Errorf("expected %q to NOT be OIDC relevant", rt)
		}
	}
}
