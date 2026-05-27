// SPDX-License-Identifier: Apache-2.0

#[quickcheck]
fn prop_reverse_reverse(xs: Vec<i32>) -> bool {
    let mut rev: Vec<i32> = xs.iter().rev().copied().collect();
    rev.reverse();
    rev == xs
}
