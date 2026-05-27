package structtree

import "testing"

// TestElement_Recursion is the T1 compile-only smoke. Builds a 2-level
// element tree to confirm pointer-recursion compiles and the field
// shape accepts the documented types.
func TestElement_Recursion(t *testing.T) {
	t.Parallel()
	child := &Element{Type: "P", Page: 0, MCIDs: []int{1}}
	root := &Element{
		Type:     "Document",
		Children: []*Element{child},
		Page:     -1,
		Attrs:    map[string]string{"Lang": "en-US"},
	}
	if got := len(root.Children); got != 1 {
		t.Fatalf("Children len = %d, want 1", got)
	}
	if root.Children[0].Type != "P" {
		t.Errorf("child.Type = %q, want P", root.Children[0].Type)
	}
	if root.Children[0].MCIDs[0] != 1 {
		t.Errorf("child.MCIDs[0] = %d, want 1", root.Children[0].MCIDs[0])
	}
	if root.Attrs["Lang"] != "en-US" {
		t.Errorf("root.Attrs[Lang] = %q, want en-US", root.Attrs["Lang"])
	}
}
