// SPDX-License-Identifier: Apache-2.0

#[cfg(fuzzing)]
fn fuzz_target(data: &[u8]) {
    let _ = std::str::from_utf8(data);
}
