# SPDX-License-Identifier: Apache-2.0

# Mock fixture: only mock-creation calls. `instance_double` and `class_double`
# classify as TestKindMock per classifyTestBlockRuby (chunker_ruby.go:71-74).
# No `it` block so no TestKindTest chunks pollute the negative assertion.
def some_mock_setup
  repo = instance_double("Repo")
  store = class_double("Store")
  [repo, store]
end
