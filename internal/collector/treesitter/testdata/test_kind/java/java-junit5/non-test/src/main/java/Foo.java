// SPDX-License-Identifier: Apache-2.0

package fixture;

import org.junit.jupiter.api.Test;

// Foo.java is a production class living under src/main/java/. Even though it
// carries @Test annotation, isJavaTestFile (chunker_jvm_shared.go:146) rejects
// the path because src/main/java/ is the production source root and the
// basename is Foo.java (no Test/Tests/IT suffix).
public class Foo {
    @Test
    public void doSomething() {}
}
