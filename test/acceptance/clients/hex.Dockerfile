# Elixir 1.20.4, Erlang/OTP 29.1.2, Alpine 3.24.3. The digest pins
# the runtime and mix; Hex is pinned separately because it ships as an archive.
FROM hexpm/elixir:1.20.4-erlang-29.1.2-alpine-3.24.3@sha256:7b15691595a49170ecea4916d6fb0074a0ac9abdde727bb2d04e0f891dc06b1a
ENV MIX_ARCHIVES=/usr/local/share/mix/archives
RUN mix local.hex 2.6.0 --force
