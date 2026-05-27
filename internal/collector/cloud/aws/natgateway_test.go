// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNATGatewayCollector_Name(t *testing.T) {
	c := &natGatewayCollector{}
	assert.Equal(t, "nat-gateway", c.Name())
}
