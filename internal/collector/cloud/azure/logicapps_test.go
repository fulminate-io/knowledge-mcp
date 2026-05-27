// SPDX-License-Identifier: Apache-2.0

package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLogicAppsCollector_Name(t *testing.T) {
	c := &logicAppsCollector{}
	assert.Equal(t, "azure-logic-apps", c.Name())
}
