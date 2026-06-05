// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fulminate-io/knowledge-mcp/internal/kgtools"
)

// openMaps is the allowlist of genuinely-arbitrary string→value object params
// that legitimately stay OPEN (no additionalProperties:false). Keyed by
// "<tool>.<param>". These five are the only open objects in the catalog: their
// keys are unbounded at the wire (analyzer knobs, metadata, free-form payloads,
// label filters), so closing them would reject valid calls. The guard skips the
// closed-object assertion for paths in this set. Source of truth: the schema
// audit + the plan's open-vs-closed lock.
var openMaps = map[string]bool{
	"query.extra":     true,
	"query.meta":      true,
	"mutate.metadata": true,
	"worker.payload":  true,
	"collect.filters": true,
}

// composerKeys are synthetic boolean-tree composers (ast.where all/any/not)
// that have no natural prose Description — they are structural, not
// content-bearing. The guard exempts them from the leaf-Description requirement
// (T4 advisory): guard for closed-object + minimal-root-Required + leaf
// descriptions, NOT for prose on structural composers.
var composerKeys = map[string]bool{
	"all": true,
	"any": true,
	"not": true,
}

// dispatchTools are the operation/type-dispatched tools whose root Required must
// hold at most the single unconditional key (operation or type). A conditional
// field in root Required would break strict validation of an otherwise-valid
// call that does not use that field. (delete + collect have no operation enum
// and a conditional shape, so their Required is empty — len<=1 covers both.)
var dispatchTools = map[string]bool{
	"mutate":   true,
	"thoughts": true,
	"ast":      true,
	"worker":   true,
	"manage":   true,
	"sync":     true,
	"collect":  true,
	"delete":   true,
}

// TestAllToolSchemas is the catalog-wide strict-validity guard. It walks
// every tool's InputSchema recursively (descending into Property.Properties and
// Property.Items) and asserts the four invariants that make the raw tools/list
// JSON strict-valid for a Codex/OpenAI-strict client:
//
//  1. every property at every depth carries a non-empty Description (the ticket's
//     core guard) — except synthetic composer keys (all/any/not);
//  2. every type:object property carrying a non-empty Properties map also sets
//     AdditionalProperties:false (closed), except the open-map allowlist;
//  3. every type:array property has a non-nil Items;
//  4. each operation/type-dispatched tool's root Required holds at most the
//     single unconditional key.
func TestAllToolSchemas(t *testing.T) {
	for _, tool := range AllToolSchemas() {
		assert.NotEmpty(t, tool.Name, "every tool must have a name")
		assert.NotEmpty(t, tool.Description, "tool %q must have a description", tool.Name)
		assert.Equal(t, "object", tool.InputSchema.Type, "tool %q root type must be object", tool.Name)

		if dispatchTools[tool.Name] {
			assert.LessOrEqual(t, len(tool.InputSchema.Required), 1,
				"dispatch tool %q root Required must hold at most the single unconditional key, got %v",
				tool.Name, tool.InputSchema.Required)
		}

		for name, prop := range tool.InputSchema.Properties {
			walkProperty(t, name, name, prop, false)
		}
	}
}

func TestAllToolSchemas_SummaryMaxLength(t *testing.T) {
	for _, tool := range AllToolSchemas() {
		for name, prop := range tool.InputSchema.Properties {
			assertSummaryMaxLength(t, tool.Name+"."+name, name, prop)
		}
	}
}

func TestCreateProjectSchema_ProjectDescriptionMaxLength(t *testing.T) {
	schema := CreateProjectToolDef().InputSchema.Properties
	description := schema["description"]
	assert.Equal(t, 249, description.MaxLength, "create_project.description must stay under Linear's 250 char cap")
}

func assertSummaryMaxLength(t *testing.T, path, key string, prop kgtools.Property) {
	t.Helper()

	if key == "summary" && prop.Type == "string" {
		assert.Equal(t, 500, prop.MaxLength, "summary property %q must declare maxLength 500", path)
	}
	for childName, child := range prop.Properties {
		assertSummaryMaxLength(t, path+"."+childName, childName, child)
	}
	if prop.Items != nil {
		assertSummaryMaxLength(t, path+"[]", key, *prop.Items)
	}
}

// walkProperty recursively checks a single property and its nested children.
// path is the dotted "<tool>.<...>" location used for the open-map allowlist and
// in failure messages; key is the immediate property name (used for the
// composer-key exemption). isItemsElem marks a node reached AS an array's Items
// element (rather than a named property) — a bare scalar element schema like
// {"type":"string"} is anonymous and conventionally carries no description, so
// the Description requirement is waived for scalar Items elements (object/array
// element shapes still require one, as do all named properties).
func walkProperty(t *testing.T, path, key string, prop kgtools.Property, isItemsElem bool) {
	t.Helper()

	// (1) Description required at every depth, except: structural composers, and
	// scalar Items elements (anonymous {"type":"string"}-style array elements).
	scalarItemsElem := isItemsElem && prop.Type != "object" && prop.Type != "array"
	if !composerKeys[key] && !scalarItemsElem {
		assert.NotEmpty(t, prop.Description, "property %q must have a Description", path)
	}

	// (2) A closed object: a type:object carrying a non-empty Properties map must
	// set AdditionalProperties:false — unless it is an allowlisted open map.
	if prop.Type == "object" && len(prop.Properties) > 0 && !openMaps[path] {
		if assert.NotNil(t, prop.AdditionalProperties,
			"closed object %q must set AdditionalProperties (false) — only the open-map allowlist may omit it", path) {
			assert.False(t, *prop.AdditionalProperties,
				"closed object %q must set AdditionalProperties:false, not true", path)
		}
	}

	// (3) Every array must declare its element shape via Items.
	if prop.Type == "array" {
		assert.NotNil(t, prop.Items, "array property %q must declare Items", path)
	}

	// Recurse into nested object sub-shapes.
	for childName, child := range prop.Properties {
		walkProperty(t, path+"."+childName, childName, child, false)
	}
	// Recurse into array element shape (and its nested object children). The
	// element is reached as an Items element (isItemsElem=true).
	if prop.Items != nil {
		walkProperty(t, path+"[]", key, *prop.Items, true)
	}
}

// TestAllToolSchemas_GuardDetectsBlankedDescription proves the guard FAILS when a
// property's Description is blanked — the regression-lock semantics the ticket
// requires.
func TestAllToolSchemas_GuardDetectsBlankedDescription(t *testing.T) {
	bad := kgtools.Property{Type: "string"} // no Description
	fake := &testing.T{}
	walkProperty(fake, "x.q", "q", bad, false)
	assert.True(t, fake.Failed(), "guard must fail on a blanked Description")
}

// TestAllToolSchemas_GuardDetectsOpenClosedObject proves the guard FAILS when a
// non-allowlisted object carries nested Properties but omits
// AdditionalProperties:false.
func TestAllToolSchemas_GuardDetectsOpenClosedObject(t *testing.T) {
	bad := kgtools.Property{Type: "object", Description: "obj", Properties: map[string]kgtools.Property{
		"k": {Type: "string", Description: "a key"},
	}} // closed object but AdditionalProperties left nil
	fake := &testing.T{}
	walkProperty(fake, "x.obj", "obj", bad, false)
	assert.True(t, fake.Failed(), "guard must fail on a closed object missing AdditionalProperties:false")
}

// TestAllToolSchemas_GuardDetectsArrayWithoutItems proves the guard FAILS when an
// array property drops its Items element shape.
func TestAllToolSchemas_GuardDetectsArrayWithoutItems(t *testing.T) {
	bad := kgtools.Property{Type: "array", Description: "a list"} // no Items
	fake := &testing.T{}
	walkProperty(fake, "x.list", "list", bad, false)
	assert.True(t, fake.Failed(), "guard must fail on an array without Items")
}

// TestAllToolSchemas_OpenMapAllowlistPasses confirms an allowlisted open map (no
// AdditionalProperties, no nested Properties) does NOT trip the closed-object
// rule — the five genuinely-arbitrary maps stay open by design.
func TestAllToolSchemas_OpenMapAllowlistPasses(t *testing.T) {
	open := kgtools.Property{Type: "object", Description: "arbitrary knobs"} // empty Properties, nil AdditionalProperties
	fake := &testing.T{}
	walkProperty(fake, "query.extra", "extra", open, false)
	assert.False(t, fake.Failed(), "an open map with empty Properties must pass the guard")
}
