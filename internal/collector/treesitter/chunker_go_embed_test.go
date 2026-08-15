// SPDX-License-Identifier: Apache-2.0

package treesitter

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGoQualifiedEmbed pins that a cross-package embed keeps its package. EMBEDS
// is one of the reference edge types the resolution walk covers, so a bare
// target reaches the own-scope rung and either misses or binds a same-named
// local type.
//
// The local `Base` row is the KNOWN-POSITIVE CONTROL: it proves the fixture
// emitted embeds at all, so the two qualified assertions cannot pass against an
// empty set. The assertion is set EQUALITY, not containment, so a spurious
// extra bare target fails.
func TestGoQualifiedEmbed(t *testing.T) {
	_, edges := chunkImportFixture(t, "app/embed.go", `package app

import (
	"example.com/mod/pkg"
	"example.com/mod/other"
)

type Base struct{}

type Holder struct {
	Base
	pkg.Remote
	*other.Ptr
}
`)

	var targets []string
	for _, e := range edges {
		if e.Type == EdgeEmbeds {
			targets = append(targets, e.ToID)
		}
	}
	sort.Strings(targets)

	assert.Equal(t, []string{"Base", "other.Ptr", "pkg.Remote"}, targets,
		"a qualified embed keeps its package, and the pointer star is not part of the target name")
}
