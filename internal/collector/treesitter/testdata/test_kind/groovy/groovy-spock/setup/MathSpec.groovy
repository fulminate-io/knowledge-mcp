// SPDX-License-Identifier: Apache-2.0

package fixture

import spock.lang.Specification

// Spock setup hooks: `def setup()` and `def setupSpec()` are recognized by
// chunker_groovy.go classifyTestKindGroovy as TestKindSetup. The string-
// literal test methods `def "..."()` parse as ERROR in tree-sitter-groovy
// and are out of scope per locked Q10.
class MathSpec extends Specification {
    def x = 1
    def y = 2

    def setup() { x = 1 }
    def setupSpec() { y = 2 }
}
