# LuaRocks 3.13.0 with current Lua 5.5.1, pinned to Alpine 3.24's immutable base.
# The native client exercises the public manifest and upload APIs in CI.
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
# LuaRocks invokes wget --timestamping, which Alpine's BusyBox wget rejects.
RUN apk add --no-cache build-base curl wget zip unzip \
    && curl -fsSL https://www.lua.org/ftp/lua-5.5.1.tar.gz -o /tmp/lua.tar.gz \
    && echo '1c4b4068d67061f2a2231ad2b5422e77acea1487ea9890f6320af614f4373dce  /tmp/lua.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/lua.tar.gz -C /tmp \
    && make -C /tmp/lua-5.5.1 linux -j2 \
    && make -C /tmp/lua-5.5.1 install \
    && curl -fsSL https://luarocks.org/releases/luarocks-3.13.0.tar.gz -o /tmp/luarocks.tar.gz \
    && echo '245bf6ec560c042cb8948e3d661189292587c5949104677f1eecddc54dbe7e37  /tmp/luarocks.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/luarocks.tar.gz -C /tmp \
    && cd /tmp/luarocks-3.13.0 \
    && ./configure --lua-version=5.5 --with-lua=/usr/local \
    && make -j2 && make install \
    && lua -v \
    && luarocks --version \
    && rm -rf /tmp/lua.tar.gz /tmp/lua-5.5.1 /tmp/luarocks.tar.gz /tmp/luarocks-3.13.0
