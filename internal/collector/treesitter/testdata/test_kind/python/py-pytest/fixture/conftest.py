# SPDX-License-Identifier: Apache-2.0

import pytest


@pytest.fixture
def user():
    return {"name": "alice"}


@pytest.fixture(scope="module")
def db_connection():
    return "conn"
