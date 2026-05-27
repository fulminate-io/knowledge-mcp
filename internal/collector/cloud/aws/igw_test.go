// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIGWCollector_Name(t *testing.T) {
	c := &igwCollector{}
	assert.Equal(t, "internet-gateway", c.Name())
}
