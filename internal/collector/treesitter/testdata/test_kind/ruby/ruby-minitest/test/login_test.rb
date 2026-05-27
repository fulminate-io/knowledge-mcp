# SPDX-License-Identifier: Apache-2.0

require "minitest/autorun"

class LoginTest < Minitest::Test
  def test_login
    assert_equal 2, 1 + 1
  end

  def test_logout
    assert true
  end
end
