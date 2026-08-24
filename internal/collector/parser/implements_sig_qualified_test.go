// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExternalLeafKeepsQualifier is the discrimination probe for the refined
// external leaf.
//
// IT IS A SYNTHETIC SIBLING OF A REAL CASE. On the frozen knowledge corpus the
// AWS API Gateway v1 and v2 clients each declare GetDomainNames over their OWN
// package's GetDomainNamesInput and GetDomainNamesOutput. Under a leaf that kept
// only the BASE name, both rendered ext:GetDomainNamesInput and the two
// signatures compared equal — a false satisfaction that only the rest of each
// interface's method set happened to prevent. This fixture strips that
// accidental protection away: each interface declares exactly ONE method, so a
// leaf that cannot tell the two packages apart derives the wrong pair and
// nothing else stops it.
//
// THE QUALIFIER IS AVAILABLE REGARDLESS OF IMPORT SPELLING, which is what makes
// the refinement possible: Go source writes apigateway.GetDomainNamesInput
// whether or not the import carries an alias, so the text before the dot
// identifies the package even when the import binds under a different name.
func TestExternalLeafKeepsQualifier(t *testing.T) {
	const v1Src = `package apigwv1

import "example.com/vendor/apigateway"

type DomainLister interface {
	GetDomainNames(in *apigateway.GetDomainNamesInput) (*apigateway.GetDomainNamesOutput, error)
}

type V1Client struct{}

func (V1Client) GetDomainNames(in *apigateway.GetDomainNamesInput) (*apigateway.GetDomainNamesOutput, error) {
	return nil, nil
}
`
	const v2Src = `package apigwv2

import "example.com/vendor/apigatewayv2"

type V2Client struct{}

func (V2Client) GetDomainNames(in *apigatewayv2.GetDomainNamesInput) (*apigatewayv2.GetDomainNamesOutput, error) {
	return nil, nil
}
`

	ix, _ := indexBoundResults(t, []fixtureFile{
		{path: "apigwv1/v1.go", src: v1Src},
		{path: "apigwv2/v2.go", src: v2Src},
	})

	spec := recFor(t, ix, "apigwv1/v1.go:DomainLister.GetDomainNames")
	v1 := recFor(t, ix, "apigwv1/v1.go:V1Client.GetDomainNames")
	v2 := recFor(t, ix, "apigwv2/v2.go:V2Client.GetDomainNames")

	require.NotEmpty(t, spec.SigKey, "control: the spec resolved a key")
	require.NotEmpty(t, v2.SigKey, "control: the v2 method resolved a key")

	// CONTROL: both external packages are genuinely outside the indexed universe,
	// so both leaves take the ext: branch. Without this the inequality below could
	// hold because one side resolved in-repo, which is a different mechanism.
	require.False(t, ix.hasScope("dir:example.com/vendor/apigateway"),
		"control: the v1 vendor package contributes no indexed declarations")
	require.False(t, ix.hasScope("dir:example.com/vendor/apigatewayv2"),
		"control: the v2 vendor package contributes no indexed declarations")

	assert.Equal(t, "(*ext:apigateway.GetDomainNamesInput)(*ext:apigateway.GetDomainNamesOutput,ext:error)",
		spec.SigKey, "an external leaf carries the qualifier AS WRITTEN alongside the base name")

	assert.NotEqual(t, spec.SigKey, v2.SigKey,
		"two DIFFERENT external packages declaring the same type name must not render one leaf — "+
			"apigateway.GetDomainNamesInput and apigatewayv2.GetDomainNamesInput are different types")

	// KNOWN-POSITIVE CONTROL, and the half that keeps the refinement honest: the
	// GENUINE implementer, whose parameter types come from the same external
	// package, still matches. An inequality assertion alone is satisfied by a
	// renderer that made every leaf unique.
	assert.Equal(t, spec.SigKey, v1.SigKey,
		"the real implementer, over the SAME external package, still renders one key")

	t.Run("derivation_pairs_only_the_real_implementer", func(t *testing.T) {
		pairs, _ := deriveImplements(ix)
		gotV1, gotV2 := false, false
		for _, p := range pairs {
			if p.iface.NodeID != "apigwv1/v1.go:DomainLister" {
				continue
			}
			switch p.concrete.NodeID {
			case "apigwv1/v1.go:V1Client":
				gotV1 = true
			case "apigwv2/v2.go:V2Client":
				gotV2 = true
			}
		}
		assert.True(t, gotV1,
			"known-positive: the same-package implementer satisfies the one-method interface")
		assert.False(t, gotV2,
			"the OTHER package's client must NOT satisfy it — a one-method interface has no other "+
				"method left to separate the two, so the leaf is the only thing that can")
	})
}

// TestExternalLeafWithoutQualifier pins the no-qualifier-written case, which the
// refinement has to define rather than leave implied.
//
// THE CHOICE: a spelling that carries NO qualifier renders ext:<base> with no
// qualifier segment, because none was written. The alternative — synthesizing
// one from the declaring file — would fabricate information the source does not
// carry, and for the case that dominates this population it would be actively
// wrong: a predeclared type belongs to Go's universe block and has no package at
// all, so `error` must render one way in every file of every package or an
// interface and its cross-package implementer stop matching on nearly every
// signature in the tree.
//
// The other members of this population are type parameters, which name no
// package either and are excluded from satisfaction by a separate rule.
//
// A qualified and an unqualified spelling of the same base name stay DISTINCT
// (ext:pkg.T versus ext:T) because the qualified form always carries the dot.
func TestExternalLeafWithoutQualifier(t *testing.T) {
	const src = `package u

import "example.com/vendor/thing"

type Contract interface {
	Predeclared(a string, b int) error
	Qualified(t thing.Payload) error
}

type Impl struct{}

func (Impl) Predeclared(a string, b int) error { return nil }

func (Impl) Qualified(t thing.Payload) error { return nil }
`

	ix, _ := indexBoundResults(t, []fixtureFile{{path: "u/u.go", src: src}})

	pre := recFor(t, ix, "u/u.go:Contract.Predeclared")
	assert.Equal(t, "(ext:string,ext:int)(ext:error)", pre.SigKey,
		"a predeclared type carries NO qualifier segment — it belongs to the universe block, not a package")

	qual := recFor(t, ix, "u/u.go:Contract.Qualified")
	assert.Equal(t, "(ext:thing.Payload)(ext:error)", qual.SigKey,
		"and a written qualifier IS carried, so the two populations stay distinguishable")

	// The cross-package property the predeclared rule exists to protect, asserted
	// where it is actually consumed rather than assumed from the shape above.
	impl := recFor(t, ix, "u/u.go:Impl.Predeclared")
	assert.Equal(t, pre.SigKey, impl.SigKey,
		"a spec and its implementer agree on every predeclared leaf")
}
