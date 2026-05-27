// SPDX-License-Identifier: Apache-2.0

package fixture

import org.junit.jupiter.api.AfterAll
import org.junit.jupiter.api.AfterEach

class TeardownTest {
    @AfterAll
    fun tearDownAll() {}

    @AfterEach
    fun tearDown() {}
}
