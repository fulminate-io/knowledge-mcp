// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAppServiceCollector_Name(t *testing.T) {
	c := &appServiceCollector{}
	assert.Equal(t, "azure-appservice", c.Name())
}
