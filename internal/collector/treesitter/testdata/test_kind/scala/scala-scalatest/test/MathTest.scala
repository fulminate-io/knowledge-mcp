// SPDX-License-Identifier: Apache-2.0

package fixture

import org.scalatest.funspec.AnyFunSpec

class MathTest extends AnyFunSpec {
  describe("addition") {
    it("adds two numbers") {
      assert(1 + 1 == 2)
    }
  }
}
