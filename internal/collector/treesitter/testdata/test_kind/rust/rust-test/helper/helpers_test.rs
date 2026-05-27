// SPDX-License-Identifier: Apache-2.0

#[cfg(test)]
mod tests {
    // helper_setup has NO #[test] attr — falls through to TestKindHelper
    // because the file's framework set contains FrameworkRustTest (added by
    // extendFrameworksRust on the #[cfg(test)] attribute).
    fn helper_setup() -> i32 {
        42
    }

    fn helper_teardown(_v: i32) {}
}
