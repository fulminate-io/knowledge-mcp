// SPDX-License-Identifier: Apache-2.0

package fixture

import io.kotest.core.spec.style.FunSpec
import io.kotest.matchers.shouldBe

class MathTests : FunSpec({
    test("adds") {
        (1 + 1) shouldBe 2
    }
    test("multiplies") {
        (2 * 3) shouldBe 6
    }
})
