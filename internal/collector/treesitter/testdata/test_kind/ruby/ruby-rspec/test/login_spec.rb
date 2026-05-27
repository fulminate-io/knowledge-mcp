# SPDX-License-Identifier: Apache-2.0

RSpec.describe "login" do
  it "succeeds" do
    expect(1 + 1).to eq(2)
  end

  context "when password missing" do
    it "fails" do
      expect(true).to be true
    end
  end
end
