// SPDX-License-Identifier: Apache-2.0

package graphtypecrud

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	knowledgev1 "github.com/fulminate-io/knowledge-mcp/gen/knowledge/v1"
)

// wire_shape_test.go — R9: the registration record's v1 WIRE SHAPE, asserted
// against the generated descriptor rather than against the Go accessors.
//
// THE FIELD NUMBERS ARE THE CONTRACT. The record is persisted as a serialized
// proto blob and decoded by a different module, so a renumbering is a silent
// mis-decode of every stored registration rather than a compile error. The
// reserved range is half of that contract: the three numbers the retired exec
// contract used must never be re-used for something else.

// fieldsOf returns the descriptor of a generated message.
func fieldsOf(m protoreflect.ProtoMessage) protoreflect.FieldDescriptors {
	return m.ProtoReflect().Descriptor().Fields()
}

func TestCollectorSpec_WireShape(t *testing.T) {
	desc := (&knowledgev1.CollectorSpec{}).ProtoReflect().Descriptor()

	// The retired exec contract's numbers AND names are reserved, so nothing can
	// re-use them and read a stored record's old bytes as something new.
	reservedNumbers := map[int32]bool{}
	for i := range desc.ReservedRanges().Len() {
		r := desc.ReservedRanges().Get(i)
		for n := r[0]; n < r[1]; n++ {
			reservedNumbers[int32(n)] = true
		}
	}
	for _, n := range []int32{1, 2, 3} {
		assert.True(t, reservedNumbers[n], "CollectorSpec field number %d must stay reserved", n)
	}
	reservedNames := map[string]bool{}
	for i := range desc.ReservedNames().Len() {
		reservedNames[string(desc.ReservedNames().Get(i))] = true
	}
	for _, name := range []string{"binary_path", "param_transport", "param_schema"} {
		assert.True(t, reservedNames[name], "CollectorSpec field name %q must stay reserved", name)
	}

	fields := desc.Fields()
	tool := fields.ByName("tool")
	require.NotNil(t, tool, "CollectorSpec must carry the tool to call")
	assert.Equal(t, protoreflect.FieldNumber(4), tool.Number())
	assert.Equal(t, protoreflect.StringKind, tool.Kind())

	stdio := fields.ByName("stdio")
	require.NotNil(t, stdio)
	assert.Equal(t, protoreflect.FieldNumber(5), stdio.Number())

	httpField := fields.ByName("http")
	require.NotNil(t, httpField)
	assert.Equal(t, protoreflect.FieldNumber(6), httpField.Number())

	// EXACTLY ONE provider, expressed as a oneof rather than two nullable fields:
	// the record cannot carry both, which is the invariant the validator states
	// and this is where it is structural rather than checked.
	require.NotNil(t, stdio.ContainingOneof(), "stdio must be part of the provider oneof")
	assert.Equal(t, "provider", string(stdio.ContainingOneof().Name()))
	assert.Equal(t, stdio.ContainingOneof(), httpField.ContainingOneof())

	// No auth in v1, anywhere on the record.
	for i := range fields.Len() {
		assert.NotContains(t, string(fields.Get(i).Name()), "auth",
			"v1 has no authentication: CollectorSpec must carry no auth field")
	}
}

func TestProviderMessages_WireShape(t *testing.T) {
	stdio := fieldsOf(&knowledgev1.StdioProvider{})
	require.NotNil(t, stdio.ByName("command"))
	assert.Equal(t, protoreflect.FieldNumber(1), stdio.ByName("command").Number())
	require.NotNil(t, stdio.ByName("args"))
	assert.Equal(t, protoreflect.FieldNumber(2), stdio.ByName("args").Number())
	assert.True(t, stdio.ByName("args").IsList())
	require.NotNil(t, stdio.ByName("env"))
	assert.Equal(t, protoreflect.FieldNumber(3), stdio.ByName("env").Number())
	assert.True(t, stdio.ByName("env").IsList(), "env is a list of variable NAMES")
	assert.Equal(t, 3, stdio.Len(), "StdioProvider carries exactly command, args and env")

	httpFields := fieldsOf(&knowledgev1.HttpProvider{})
	require.NotNil(t, httpFields.ByName("url"))
	assert.Equal(t, protoreflect.FieldNumber(1), httpFields.ByName("url").Number())
	assert.Equal(t, 1, httpFields.Len(),
		"v1's http provider is a URL and nothing else — no auth field is introduced")
}

// TestNoProviderAuthMessage pins the absence R4 is about, at the one place it is
// decidable: the generated file's own message registry. Finding the type would
// mean the wire grew an auth shape v1 does not have.
func TestNoProviderAuthMessage(t *testing.T) {
	files := (&knowledgev1.CollectorSpec{}).ProtoReflect().Descriptor().ParentFile()
	messages := files.Messages()
	for i := range messages.Len() {
		name := string(messages.Get(i).Name())
		assert.NotContains(t, name, "Auth",
			"v1 introduces no authentication message on the registration wire; found %q", name)
		assert.NotEqual(t, "ParamSpec", name,
			"ParamSpec went with the retired exec contract's param_schema")
	}
}
