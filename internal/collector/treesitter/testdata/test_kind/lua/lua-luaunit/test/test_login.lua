-- SPDX-License-Identifier: Apache-2.0

local TestLogin = {}

function TestLogin:testLogin()
  luaunit.assertEquals(2, 1 + 1)
end

return TestLogin
