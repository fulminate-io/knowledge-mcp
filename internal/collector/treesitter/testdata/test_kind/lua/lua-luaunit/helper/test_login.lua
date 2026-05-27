-- SPDX-License-Identifier: Apache-2.0

local TestLogin = {}

-- Method on a Test* table without test_/Test prefix → TestKindHelper.
function TestLogin:make_user(name)
  return { name = name }
end

return TestLogin
