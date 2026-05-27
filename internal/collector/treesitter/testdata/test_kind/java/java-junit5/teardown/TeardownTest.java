// SPDX-License-Identifier: Apache-2.0

package fixture;

import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;

public class TeardownTest {
    @AfterAll
    static void tearDownAll() {}

    @AfterEach
    void tearDown() {}
}
