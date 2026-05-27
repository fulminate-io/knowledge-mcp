// SPDX-License-Identifier: Apache-2.0

package fixture

import munit.FunSuite

class SetupTest extends FunSuite {
  beforeAll {
    val _ = 1
  }
  beforeEach {
    val _ = 2
  }
}
