#!/usr/bin/env bats

# SPDX-License-Identifier: Apache-2.0

# Per locked Q5 (degraded path), tree-sitter-bash fragments @test directives
# into separate command nodes; Bucket A applies filename-only classification.
# Any declaration in this .bats file → TestKindTest unless name matches
# setup/teardown.

@test "login succeeds" {
  result=$((1 + 1))
  [ "$result" -eq 2 ]
}

run_login_test() {
  echo "delegated"
}
