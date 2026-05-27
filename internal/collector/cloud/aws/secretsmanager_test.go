// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecretsManagerCollector_Name(t *testing.T) {
	c := &secretsManagerCollector{}
	assert.Equal(t, "secretsmanager", c.Name())
}
