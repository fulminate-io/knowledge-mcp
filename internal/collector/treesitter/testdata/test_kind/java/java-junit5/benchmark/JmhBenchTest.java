// SPDX-License-Identifier: Apache-2.0

package fixture;

import org.openjdk.jmh.annotations.Benchmark;

// JmhBenchTest.java — `Test.java` suffix satisfies isJavaTestFile gate
// (chunker_jvm_shared.go:146); @Benchmark dispatch via jvmAnnotationKind
// (chunker_java.go:69) returns TestKindBenchmark.
public class JmhBenchTest {
    @Benchmark
    public void measureParseUrl() {}
}
