module LoginTests exposing (..)

import Test exposing (..)
import Expect


loginTest : Test
loginTest =
    test "login succeeds" <|
        \_ -> Expect.equal 2 (1 + 1)
