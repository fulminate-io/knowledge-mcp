#!/usr/bin/env bats

# SPDX-License-Identifier: Apache-2.0

teardown() {
  unset TEST_VAR
}

teardown_file() {
  unset GLOBAL_VAR
}
