// SPDX-License-Identifier: Apache-2.0

use proptest::prelude::*;

// Canonical proptest idiom: #[test] wrapper around a proptest! body. The
// outer #[test] is the attribute the predicate classifies on; proptest!
// generates the inner property-test loop.
#[test]
fn prop_invariant() {
    proptest!(|(x in 0..100i32)| {
        prop_assert!(x >= 0);
    });
}
