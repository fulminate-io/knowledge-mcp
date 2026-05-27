// SPDX-License-Identifier: Apache-2.0

package gcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMemorystoreSubCollector_Name(t *testing.T) {
	c := &memorystoreSubCollector{}
	assert.Equal(t, "gcp-memorystore", c.Name())
}
