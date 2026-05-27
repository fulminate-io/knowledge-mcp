# SPDX-License-Identifier: Apache-2.0

run "valid_inputs" {
  variables {
    bucket_name = "test-bucket"
  }
  assert {
    condition     = true
    error_message = "expected ok"
  }
}
