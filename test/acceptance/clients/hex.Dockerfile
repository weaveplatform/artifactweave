# Client pins: Hex 2.6.0; Elixir 1.20.5; OTP 29.1.2.
# The image digest pins the Elixir, Erlang/OTP and Alpine runtime and mix;
# Hex is pinned separately because it ships as an archive. Dependabot cannot
# list hexpm/elixir's tags, so deps-refresh.yml moves this base with
# tools/depsync/hex_image.py.
FROM hexpm/elixir:1.20.5-erlang-29.1.2-alpine-3.24.4@sha256:fdc3e2838224d974d854eb5c0ec4cd790da31761ebebf5fbdd22113f19eb0002
ENV MIX_ARCHIVES=/usr/local/share/mix/archives
RUN mix local.hex 2.6.0 --force

# Retain the installed client identity in the native image build log.
RUN HEX_OFFLINE=1 mix hex.info
