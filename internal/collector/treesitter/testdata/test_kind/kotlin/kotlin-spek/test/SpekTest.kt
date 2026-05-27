// SPDX-License-Identifier: Apache-2.0

package fixture

import org.spekframework.spek2.Spek
import org.spekframework.spek2.style.specification.describe

class CalcTests : Spek({
    describe("calculator") {
        it("adds") {
            assert((1 + 1) == 2)
        }
    }
})
