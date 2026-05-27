// SPDX-License-Identifier: Apache-2.0

using Microsoft.VisualStudio.TestTools.UnitTesting;

[TestClass]
public class TeardownTests {
    [TestCleanup]
    public void Teardown() {}
}
