// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFunctionsCollector_Name(t *testing.T) {
	c := &functionsCollector{}
	assert.Equal(t, "azure-functions", c.Name())
}

func TestIsFunctionApp(t *testing.T) {
	tests := []struct {
		name string
		kind *string
		want bool
	}{
		{"nil kind", nil, false},
		{"exact match", new("functionapp"), true},
		{"with linux suffix", new("functionapp,linux"), true},
		{"with container suffix", new("functionapp,linux,container"), true},
		{"uppercase", new("FUNCTIONAPP"), true},
		{"mixed case with suffix", new("FunctionApp,Linux"), true},
		{"with spaces", new("functionapp, linux"), true},
		{"regular web app", new("app"), false},
		{"web app linux", new("app,linux"), false},
		{"empty string", new(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isFunctionApp(tt.kind))
		})
	}
}
