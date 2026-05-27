// SPDX-License-Identifier: Apache-2.0

package fixture;

import org.junit.After;
import org.junit.AfterClass;

public class TeardownTest {
    @After
    public void tearDown() {}

    @AfterClass
    public static void tearDownAll() {}
}
