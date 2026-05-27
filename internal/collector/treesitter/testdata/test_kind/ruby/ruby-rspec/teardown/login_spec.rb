# SPDX-License-Identifier: Apache-2.0

RSpec.describe "login" do
  after(:each) do
    @user = nil
  end

  after(:all) do
    @db = nil
  end
end
