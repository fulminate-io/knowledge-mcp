// SPDX-License-Identifier: Apache-2.0

package mocks

// Files under any path component named `mocks/` are classified TestKindMock
// per chunker_go.go:130-140. The walker treats the outer `mock/` as the kind
// directory; `mocks/` is opaque tail passed through verbatim.

type Store struct{}

func NewStore() *Store { return &Store{} }

func (s *Store) Get(k string) string { return k }
