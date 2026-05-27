// SPDX-License-Identifier: Apache-2.0

#include <gmock/gmock.h>

// mock_*.cc filename triggers isCppMockFile — every declaration classifies
// as TestKindMock.
class MockRepo {
 public:
  MOCK_METHOD(int, Get, (int key), ());
  MOCK_METHOD(void, Set, (int key, int value), ());
};
