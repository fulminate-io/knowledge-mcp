# SPDX-License-Identifier: Apache-2.0

import unittest


class TestWithHelper(unittest.TestCase):
    def helper_for(self, name):
        return f"helper-{name}"

    def make_user(self, name):
        return {"name": name}
