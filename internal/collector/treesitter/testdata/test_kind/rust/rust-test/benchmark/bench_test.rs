// SPDX-License-Identifier: Apache-2.0

#![feature(test)]

extern crate test;
use test::Bencher;

#[bench]
fn bench_parse(b: &mut Bencher) {
    b.iter(|| {
        let _ = "https://example.com".len();
    });
}
