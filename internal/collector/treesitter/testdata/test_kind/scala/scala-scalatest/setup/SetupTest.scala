// SPDX-License-Identifier: Apache-2.0

package fixture

import org.scalatest.funsuite.AnyFunSuite

// ScalaTest setup uses call-expression beforeAll/beforeEach forms; the Bucket
// B test_block predicate (chunker_scala.go classifyTestBlockScala) classifies
// these calls as TestKindSetup.
class SetupTest extends AnyFunSuite {
  beforeAll {
    val _ = 1
  }
  beforeEach {
    val _ = 2
  }
}
