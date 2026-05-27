// SPDX-License-Identifier: Apache-2.0

package fixture;

import org.testng.annotations.BeforeClass;
import org.testng.annotations.BeforeMethod;

public class SetupTest {
    @BeforeClass
    public void setUpAll() {}

    @BeforeMethod
    public void setUp() {}
}
