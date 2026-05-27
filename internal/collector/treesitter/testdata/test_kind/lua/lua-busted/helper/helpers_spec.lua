-- SPDX-License-Identifier: Apache-2.0

-- Top-level functions in a *_spec.lua file classify as TestKindHelper per
-- classifyTestKindLua (chunker_lua.go:69 — empty tableName fall-through).
function helper_for(name)
  return "helper-" .. name
end
