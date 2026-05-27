// SPDX-License-Identifier: Apache-2.0

#include <string>

// Helper functions in a *_test.cc file — classifyTestKindCpp returns
// TestKindHelper for any declaration in a recognized test file.
std::string make_user(const std::string& name) {
  return "user-" + name;
}
