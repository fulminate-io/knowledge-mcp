# SPDX-License-Identifier: Apache-2.0

import pytest


# autouse=True fixture is recognized as TestKindSetup per
# pytestFixtureDecoratorKind (chunker_python.go:105).
@pytest.fixture(autouse=True)
def setup_each_test():
    return None
