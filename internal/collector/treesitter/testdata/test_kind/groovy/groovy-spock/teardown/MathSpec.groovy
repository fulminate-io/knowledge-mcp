// SPDX-License-Identifier: Apache-2.0

package fixture

import spock.lang.Specification

class MathSpec extends Specification {
    def z = 3
    def w = 4

    def cleanup() { z = 3 }
    def cleanupSpec() { w = 4 }
}
