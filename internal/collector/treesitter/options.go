// SPDX-License-Identifier: Apache-2.0

package treesitter

type chunkerConfig struct {
	includeContext bool
}

func defaultConfig() *chunkerConfig {
	return &chunkerConfig{
		includeContext: true,
	}
}
