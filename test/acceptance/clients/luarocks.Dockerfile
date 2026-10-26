# LuaRocks 3.11.0 with Lua 5.4, pinned to Alpine 3.20's immutable base.
# The native client exercises the public manifest and upload APIs in CI.
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache luarocks5.4=3.11.0-r0 lua5.4=5.4.6-r1 zip unzip
