// SPDX-License-Identifier: Apache-2.0

use rstest::rstest;

#[rstest]
fn test_with_params(#[case] x: i32) {
    assert!(x >= 0);
}
