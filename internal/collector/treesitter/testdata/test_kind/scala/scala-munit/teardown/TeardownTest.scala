// SPDX-License-Identifier: Apache-2.0

package fixture

import munit.FunSuite

class TeardownTest extends FunSuite {
  afterAll {
    val _ = 1
  }
  afterEach {
    val _ = 2
  }
}
