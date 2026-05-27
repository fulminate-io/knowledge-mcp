// SPDX-License-Identifier: Apache-2.0

#include <gtest/gtest.h>

TEST(LoginSuite, Succeeds) {
  EXPECT_EQ(2, 1 + 1);
}

TEST_F(LoginFixture, WithFixture) {
  EXPECT_TRUE(true);
}
