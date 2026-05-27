# SPDX-License-Identifier: Apache-2.0

# spec_helper.rb is a typical helper file. The Bucket B predicate emits
# test_block chunks only for recognized RSpec calls; declaration chunks (def
# foo / module Bar / class Baz) inside this file are NOT classified by
# Bucket A (Ruby's Bucket A only fires for classes extending Minitest::Test
# / Test::Unit::TestCase). Per the negative-only contract, this file must
# carry at least ONE chunk classified TestKindHelper. We use the Bucket B
# fixture-mock-helper convention: hooks like `before` / `let` aren't quite
# helpers in the chunker's enum sense — but a `it` block whose body only
# contains helper code is still TestKindTest. So instead we use a CLASSIC
# Minitest helper-style file path. But the directory is ruby/ruby-rspec/
# helper/. The file is named spec_helper.rb (under spec/-style path); the
# predicate emits test_block chunks that aren't TestKindHelper.
#
# Workaround: include a `class FooHelper < Minitest::Test` with a non-test_*
# named method to surface a TestKindHelper chunk via Bucket A (Ruby is
# dual-bucket). The file lives in spec/ so isRubyTestFile fires.
class FooHelper < Minitest::Test
  def shared_setup
    "ok"
  end
end
