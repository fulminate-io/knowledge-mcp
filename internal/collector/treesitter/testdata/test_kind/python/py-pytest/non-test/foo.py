# SPDX-License-Identifier: Apache-2.0

# foo.py — basename does not start with test_, does not end with _test.py,
# is not conftest.py. Path has no tests/ or test/ segment from the absolute
# fixture root through here (the walker passes the full absolute path
# starting from the OS root). isPythonTestFile rejects this file; all
# def test_* declarations classify (false, TestKindNone).
def test_one():
    pass


def test_two():
    pass
