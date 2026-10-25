# Elixir 1.16.3, Erlang/OTP 26.2.5.21, Alpine 3.24.3. The digest pins
# the runtime and mix; Hex is pinned separately because it ships as an archive.
FROM hexpm/elixir@sha256:db31c203516f396b2127e477aadca2de9ca7c91efdc3302c8b9a947d24d22283
ENV MIX_ARCHIVES=/usr/local/share/mix/archives
RUN mix local.hex 2.6.0 --force
