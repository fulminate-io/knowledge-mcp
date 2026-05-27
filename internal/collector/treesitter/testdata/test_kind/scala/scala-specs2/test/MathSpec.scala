// SPDX-License-Identifier: Apache-2.0

package fixture

import org.specs2.mutable.Specification

class MathSpec extends Specification {
  "adding" should {
    "work" in {
      (1 + 1) must_== 2
    }
  }
}
