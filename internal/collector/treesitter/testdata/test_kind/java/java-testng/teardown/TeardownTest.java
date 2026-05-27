// SPDX-License-Identifier: Apache-2.0

package fixture;

import org.testng.annotations.AfterClass;
import org.testng.annotations.AfterMethod;

public class TeardownTest {
    @AfterClass
    public void tearDownAll() {}

    @AfterMethod
    public void tearDown() {}
}
