# SPDX-License-Identifier: Apache-2.0

# In a test_*.py file, a function NOT named test_*, setUp/tearDown, and NOT
# decorated with @pytest.fixture classifies as TestKindHelper per
# classifyTestKindPython (chunker_python.go:170).
def helper_normalize(s):
    return s.strip().lower()


def make_user(name):
    return {"name": name}
