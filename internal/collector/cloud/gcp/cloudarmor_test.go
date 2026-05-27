// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCloudArmorSubCollector_Name(t *testing.T) {
	c := &cloudArmorSubCollector{}
	assert.Equal(t, "gcp-cloud-armor", c.Name())
}
