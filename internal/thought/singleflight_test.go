// SPDX-License-Identifier: Apache-2.0

package thought

import "testing"

// TestAcquireReflectionPass_Coalesce asserts the single-flight contract: the
// first claim succeeds, a concurrent second claim coalesces (ok==false, nil
// release), and after the first claim releases a third claim succeeds again.
func TestAcquireReflectionPass_Coalesce(t *testing.T) {
	const key = "knowledge/test-singleflight"

	release1, ok1 := AcquireReflectionPass(key)
	if !ok1 || release1 == nil {
		t.Fatalf("first claim: got (release!=nil=%v, ok=%v), want (true, true)", release1 != nil, ok1)
	}

	// Second claim while the key is held coalesces.
	release2, ok2 := AcquireReflectionPass(key)
	if ok2 || release2 != nil {
		t.Fatalf("concurrent second claim: got (release!=nil=%v, ok=%v), want (nil, false)", release2 != nil, ok2)
	}

	// Release frees the key.
	release1()

	// A third claim after release succeeds.
	release3, ok3 := AcquireReflectionPass(key)
	if !ok3 || release3 == nil {
		t.Fatalf("third claim after release: got (release!=nil=%v, ok=%v), want (true, true)", release3 != nil, ok3)
	}
	release3()
}
