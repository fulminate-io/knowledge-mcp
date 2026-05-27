# SPDX-License-Identifier: Apache-2.0

# Fixture fixture: only let / let! / subject calls. These classify as
# TestKindFixture per classifyTestBlockRuby (chunker_ruby.go:69-70). No
# `describe`/`it` to avoid TestKindTest chunks polluting the negative
# assertion.
let(:user) { "alice" }
let!(:eager_user) { "bob" }
subject { build_session }
