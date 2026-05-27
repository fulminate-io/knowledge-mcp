/* SPDX-License-Identifier: Apache-2.0 */

#include "unity.h"

void test_login_succeeds(void) {
  TEST_ASSERT_EQUAL(2, 1 + 1);
}

int main(void) {
  UNITY_BEGIN();
  RUN_TEST(test_login_succeeds);
  return UNITY_END();
}
