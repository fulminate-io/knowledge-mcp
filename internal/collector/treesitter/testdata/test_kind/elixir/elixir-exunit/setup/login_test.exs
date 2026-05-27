# SPDX-License-Identifier: Apache-2.0

defmodule LoginTest do
  use ExUnit.Case

  setup do
    %{user: "alice"}
  end

  setup_all do
    %{db: "conn"}
  end
end
