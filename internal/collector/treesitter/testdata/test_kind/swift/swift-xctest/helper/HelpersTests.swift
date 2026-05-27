// SPDX-License-Identifier: Apache-2.0

import XCTest

final class HelpersTests: XCTestCase {
    func makeUser(name: String) -> String {
        return "user-" + name
    }
}
