# SPDX-License-Identifier: Apache-2.0

require "minitest/autorun"

class LoginTest < Minitest::Test
  def teardown
    @user = nil
  end
end
