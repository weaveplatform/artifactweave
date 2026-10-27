# LuaRocks 3.12.2 with Lua 5.4, pinned to Alpine 3.24's immutable base.
# The native client exercises the public manifest and upload APIs in CI.
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache luarocks5.4=3.12.2-r1 lua5.4=5.4.8-r0 zip unzip
