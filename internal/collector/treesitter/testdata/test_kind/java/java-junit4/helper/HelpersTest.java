// SPDX-License-Identifier: Apache-2.0

package fixture;

// In a *Test.java file, methods without recognized annotations classify as
// TestKindHelper per classifyTestKindJava (chunker_java.go:47). The class
// itself is also TestKindHelper per the class-declaration arm.
public class HelpersTest {
    public String fixtureFor(String name) {
        return "fixture-" + name;
    }
}
