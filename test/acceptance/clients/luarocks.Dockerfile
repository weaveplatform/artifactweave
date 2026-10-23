# LuaRocks 3.11.0 with Lua 5.4, pinned to Alpine 3.20's immutable base.
# The native client exercises the public manifest and upload APIs in CI.
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
RUN apk add --no-cache luarocks5.4=3.11.0-r0 lua5.4=5.4.6-r1 zip unzip
