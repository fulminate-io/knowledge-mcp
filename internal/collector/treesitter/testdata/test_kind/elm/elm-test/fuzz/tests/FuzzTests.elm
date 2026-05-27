module FuzzTests exposing (..)

import Test exposing (..)
import Fuzz
import Expect


fuzzTest : Test
fuzzTest =
    fuzz Fuzz.int "addition is commutative" <|
        \n -> Expect.equal (n + 1) (1 + n)
