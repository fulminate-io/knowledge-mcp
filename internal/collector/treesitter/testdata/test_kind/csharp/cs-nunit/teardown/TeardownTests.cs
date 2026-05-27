// SPDX-License-Identifier: Apache-2.0

using NUnit.Framework;

public class TeardownTests {
    [TearDown]
    public void TearDown() {}

    [OneTimeTearDown]
    public void OneTimeTearDown() {}
}
