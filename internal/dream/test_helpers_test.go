// SPDX-License-Identifier: Apache-2.0

package dream

import (
	"testing"
)

// registryWith returns a Registry seeded with the supplied workers via
// a fakeLister. Used by runner_test.go to construct a Registry without
// a real GraphClient or workercrud.Client — the post-BCN3 equivalent
// of the prior wire-shape JSON harness.
func registryWith(t testing.TB, ws ...Worker) *Registry {
	t.Helper()
	return NewRegistry(&fakeLister{workers: append([]Worker(nil), ws...)})
}
