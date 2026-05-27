// SPDX-License-Identifier: Apache-2.0

package fixture

import kotlinx.benchmark.Benchmark

// kotlinx-benchmark uses @Benchmark — same simple-name annotation as JMH;
// classifies as TestKindBenchmark via shared jvmAnnotationKind dispatch.
class BenchTest {
    @Benchmark
    fun measure() {}
}
