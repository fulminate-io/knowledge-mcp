// SPDX-License-Identifier: Apache-2.0

package fixture

// MockDB is a hand-rolled mock. The `_mock.go` filename suffix triggers
// isGoMockFile (chunker_go.go:130) regardless of `_test.go` presence.
type MockDB struct{}

func NewMockDB() *MockDB { return &MockDB{} }

func (m *MockDB) Query(s string) string { return s }
