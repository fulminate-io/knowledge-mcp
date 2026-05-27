# SPDX-License-Identifier: Apache-2.0

# pytest doesn't have a separate teardown decorator; teardown is
# expressed via yield-based fixtures with cleanup AFTER the yield. Bucket A's
# only Teardown signal is unittest's tearDown/tearDownClass methods. Use
# unittest-style here under py-pytest path because pytest can run unittest
# classes; the predicate doesn't gate on framework.
import unittest


class TestSomething(unittest.TestCase):
    def tearDown(self):
        pass

    def tearDownClass(cls):
        pass
