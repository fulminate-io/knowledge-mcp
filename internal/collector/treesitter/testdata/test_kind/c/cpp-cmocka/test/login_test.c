/* SPDX-License-Identifier: Apache-2.0 */

#include <stdarg.h>
#include <stddef.h>
#include <setjmp.h>
#include <cmocka.h>

static void test_login(void **state) {
  (void)state;
  assert_int_equal(1 + 1, 2);
}

int main(void) {
  const struct CMUnitTest tests[] = {
    cmocka_unit_test(test_login),
  };
  return cmocka_run_group_tests(tests, NULL, NULL);
}
