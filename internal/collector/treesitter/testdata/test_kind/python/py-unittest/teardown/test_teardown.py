# SPDX-License-Identifier: Apache-2.0

import unittest


class TestWithTeardown(unittest.TestCase):
    def tearDown(self):
        pass

    @classmethod
    def tearDownClass(cls):
        pass
