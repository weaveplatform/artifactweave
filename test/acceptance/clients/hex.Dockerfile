# Client pins: Hex 2.6.0; Elixir 1.20.5; OTP 29.1.3.
# The image digest pins the Elixir, Erlang/OTP and Alpine runtime and mix;
# Hex is pinned separately because it ships as an archive. Dependabot cannot
# list hexpm/elixir's tags, so deps-refresh.yml moves this base with
# tools/depsync/hex_image.py.
FROM hexpm/elixir:1.20.5-erlang-29.1.3-alpine-3.24.4@sha256:f6f8f3ee86a523d4e97d8bbb584ff625a831c1258cbac1b97f6735696373f93d
ENV MIX_ARCHIVES=/usr/local/share/mix/archives
RUN mix local.hex 2.6.0 --force

# Retain the installed client identity in the native image build log.
RUN HEX_OFFLINE=1 mix hex.info
