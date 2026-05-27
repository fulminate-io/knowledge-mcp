# SPDX-License-Identifier: Apache-2.0

import unittest


class TestWithSetup(unittest.TestCase):
    def setUp(self):
        self.x = 1

    @classmethod
    def setUpClass(cls):
        cls.y = 2
