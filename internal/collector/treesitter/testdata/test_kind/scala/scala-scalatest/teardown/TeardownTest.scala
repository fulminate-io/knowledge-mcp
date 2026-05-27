// SPDX-License-Identifier: Apache-2.0

package fixture

import org.scalatest.funsuite.AnyFunSuite

class TeardownTest extends AnyFunSuite {
  afterAll {
    val _ = 1
  }
  afterEach {
    val _ = 2
  }
}
