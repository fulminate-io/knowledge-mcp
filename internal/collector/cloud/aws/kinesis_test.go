// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKinesisCollector_Name(t *testing.T) {
	c := &kinesisCollector{}
	assert.Equal(t, "kinesis", c.Name())
}
