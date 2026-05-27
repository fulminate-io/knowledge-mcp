#!/usr/bin/env bats

# SPDX-License-Identifier: Apache-2.0

setup() {
  export TEST_VAR=1
}

setup_file() {
  export GLOBAL_VAR=2
}
