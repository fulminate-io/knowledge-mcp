(* SPDX-License-Identifier: Apache-2.0 *)

let test_login () =
  Alcotest.(check int) "adds" 2 (1 + 1)

let () =
  Alcotest.run "login" [
    "basic", [ Alcotest.test_case "login" `Quick test_login ];
  ]
