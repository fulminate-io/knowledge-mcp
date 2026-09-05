// SPDX-License-Identifier: Apache-2.0

package web

import (
	"encoding/json"
	"reflect"
	"testing"

	"golang.org/x/net/html"
)

// attrNode builds a minimal *html.Node with the given attributes. Direct
// construction keeps these tests independent of html.Parse so failures
// point at the attribute helpers, not the parser.
func attrNode(attrs ...html.Attribute) *html.Node {
	return &html.Node{
		Type: html.ElementNode,
		Data: "div",
		Attr: attrs,
	}
}

func TestExtractCommonAttrs_AllFields(t *testing.T) {
	n := attrNode(
		html.Attribute{Key: "class", Val: "foo bar"},
		html.Attribute{Key: "id", Val: "main"},
		html.Attribute{Key: "role", Val: "navigation"},
		html.Attribute{Key: "data-foo", Val: "bar"},
		html.Attribute{Key: "data-baz", Val: "qux"},
	)
	got := extractCommonAttrs(n)
	if got.Class != "foo bar" {
		t.Errorf("Class = %q, want %q", got.Class, "foo bar")
	}
	if got.ID != "main" {
		t.Errorf("ID = %q, want %q", got.ID, "main")
	}
	if got.Role != "navigation" {
		t.Errorf("Role = %q, want %q", got.Role, "navigation")
	}
	wantData := map[string]string{"foo": "bar", "baz": "qux"}
	if !reflect.DeepEqual(got.Data, wantData) {
		t.Errorf("Data = %v, want %v", got.Data, wantData)
	}
}

// TestExtractCommonAttrs_EmptyNode pins the ATTRIBUTE half of the extract on an
// element carrying no attributes at all.
//
// It deliberately does NOT assert the zero value any more. Tag and DomDepth are
// read from the ELEMENT rather than from its attributes, so an attribute-free
// element legitimately carries both — asserting the zero value here would be
// asserting that the two universal signals are absent on exactly the elements
// that most need them.
func TestExtractCommonAttrs_EmptyNode(t *testing.T) {
	n := attrNode()
	got := extractCommonAttrs(n)
	if !reflect.DeepEqual(commonAttrs{Class: got.Class, ID: got.ID, Role: got.Role, Data: got.Data}, commonAttrs{}) {
		t.Errorf("expected every attribute-derived field empty, got %+v", got)
	}
	if got.Tag != "div" {
		t.Errorf("Tag = %q, want %q — the tag comes from the element, not its attributes", got.Tag, "div")
	}
	// An element-derived record reports its OWN element as the attribute
	// source and names no ancestor it climbed to, which is what keeps
	// "ancestor" a statement about a real climb rather than a default.
	md := map[string]string{}
	applyCommonAttrs(md, got)
	if md["attr_source"] != attrSourceOwn {
		t.Errorf("attr_source = %q, want %q for an element-derived record", md["attr_source"], attrSourceOwn)
	}
	if _, named := md["attr_source_tag"]; named {
		t.Errorf("an element-derived record named a source it climbed to: %q", md["attr_source_tag"])
	}
	if md["dom_depth"] == "" {
		t.Errorf("an attribute-free element must still carry a dom_depth")
	}
}

func TestExtractCommonAttrs_NilNode(t *testing.T) {
	got := extractCommonAttrs(nil)
	if !reflect.DeepEqual(got, commonAttrs{}) {
		t.Errorf("expected zero-value commonAttrs for nil, got %+v", got)
	}
}

func TestExtractCommonAttrs_OnlyData(t *testing.T) {
	n := attrNode(
		html.Attribute{Key: "data-x", Val: "1"},
		html.Attribute{Key: "data-y", Val: "2"},
	)
	got := extractCommonAttrs(n)
	if got.Class != "" || got.ID != "" || got.Role != "" {
		t.Errorf("expected empty Class/ID/Role, got %+v", got)
	}
	wantData := map[string]string{"x": "1", "y": "2"}
	if !reflect.DeepEqual(got.Data, wantData) {
		t.Errorf("Data = %v, want %v", got.Data, wantData)
	}
}

func TestApplyCommonAttrs_SkipsEmpty(t *testing.T) {
	md := map[string]string{}
	applyCommonAttrs(md, commonAttrs{})
	if len(md) != 0 {
		t.Errorf("expected empty map after applying zero-value attrs, got %v", md)
	}
}

func TestApplyCommonAttrs_JSONEncodesData(t *testing.T) {
	md := map[string]string{}
	data := map[string]string{"foo": "bar", "baz": "qux"}
	applyCommonAttrs(md, commonAttrs{Data: data})
	raw, ok := md["data"]
	if !ok {
		t.Fatalf("expected md[\"data\"] to be set, got %v", md)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("md[\"data\"] is not valid JSON: %v (raw=%q)", err, raw)
	}
	if !reflect.DeepEqual(decoded, data) {
		t.Errorf("decoded data = %v, want %v", decoded, data)
	}
	// Other keys must not be set when only Data is provided.
	if _, ok := md["class"]; ok {
		t.Errorf("expected md[\"class\"] absent, got %q", md["class"])
	}
	if _, ok := md["id"]; ok {
		t.Errorf("expected md[\"id\"] absent, got %q", md["id"])
	}
	if _, ok := md["role"]; ok {
		t.Errorf("expected md[\"role\"] absent, got %q", md["role"])
	}
}

func TestApplyCommonAttrs_MergesClassIDRole(t *testing.T) {
	md := map[string]string{
		"existing": "untouched",
		"kind":     "section",
	}
	applyCommonAttrs(md, commonAttrs{
		Class: "primary",
		ID:    "intro",
		Role:  "main",
	})
	if md["existing"] != "untouched" {
		t.Errorf("existing key clobbered: %q", md["existing"])
	}
	if md["kind"] != "section" {
		t.Errorf("kind key clobbered: %q", md["kind"])
	}
	if md["class"] != "primary" {
		t.Errorf("class = %q, want %q", md["class"], "primary")
	}
	if md["id"] != "intro" {
		t.Errorf("id = %q, want %q", md["id"], "intro")
	}
	if md["role"] != "main" {
		t.Errorf("role = %q, want %q", md["role"], "main")
	}
	if _, ok := md["data"]; ok {
		t.Errorf("expected md[\"data\"] absent when Data is nil, got %q", md["data"])
	}
}
