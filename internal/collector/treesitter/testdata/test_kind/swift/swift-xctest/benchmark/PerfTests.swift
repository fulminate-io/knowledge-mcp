// SPDX-License-Identifier: Apache-2.0

import XCTest

final class PerfTests: XCTestCase {
    func testSumPerformance() {
        measure {
            _ = (0..<1000).reduce(0, +)
        }
    }

    func testSumMetrics() {
        measureMetrics([XCTPerformanceMetric.wallClockTime], automaticallyStartMeasuring: true) {
            startMeasuring()
            _ = (0..<1000).reduce(0, +)
            stopMeasuring()
        }
    }

    // Not a benchmark. This trailing-closure call is the one the TestBlocks
    // #match? predicate must reject, which is what keeps withCaptures below raw
    // for this corpus and makes the Swift pin meaningful.
    func testAutoreleaseIsNotABenchmark() {
        autoreleasepool {
            _ = (0..<10).reduce(0, +)
        }
    }
}
