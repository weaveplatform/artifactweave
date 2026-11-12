# Client pins: LuaRocks 3.13.0; Lua 5.5.1.
# The native client exercises the public manifest and upload APIs in CI.
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
# LuaRocks invokes wget --timestamping, which Alpine's BusyBox wget rejects.
# Each tarball has a second source so one host being down does not fail the build
# (#1321); the SHA-256 check holds for whichever answers. www.lua.org often resets TLS
# or times out, so Lua falls back to PUC-Rio, the mirror lua.org's mirrors page names.
# Its copy of the download area is /lua/ftp/; the page's /lua/mirror/ link is only a
# landing page. luarocks.org only redirects to the GitHub Pages release area, which
# the v3.13.0 GitHub release names because it has no assets, so LuaRocks falls back
# to that area directly. Alpine's packages are no fallback: 3.24 ships Lua 5.5.0 and
# LuaRocks 3.12.2, not the pinned releases.
RUN apk add --no-cache build-base curl wget zip unzip \
    && { curl -fsSL --connect-timeout 15 --max-time 60 --retry 3 --retry-delay 2 --retry-max-time 180 --retry-all-errors \
        https://www.lua.org/ftp/lua-5.5.1.tar.gz -o /tmp/lua.tar.gz \
      || curl -fsSL --connect-timeout 15 --max-time 60 --retry 3 --retry-delay 2 --retry-max-time 180 --retry-all-errors \
        https://www.tecgraf.puc-rio.br/lua/ftp/lua-5.5.1.tar.gz -o /tmp/lua.tar.gz; } \
    && echo '1c4b4068d67061f2a2231ad2b5422e77acea1487ea9890f6320af614f4373dce  /tmp/lua.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/lua.tar.gz -C /tmp \
    && make -C /tmp/lua-5.5.1 linux -j2 \
    && make -C /tmp/lua-5.5.1 install \
    && { curl -fsSL --connect-timeout 15 --max-time 60 --retry 3 --retry-delay 2 --retry-max-time 180 --retry-all-errors \
        https://luarocks.org/releases/luarocks-3.13.0.tar.gz -o /tmp/luarocks.tar.gz \
      || curl -fsSL --connect-timeout 15 --max-time 60 --retry 3 --retry-delay 2 --retry-max-time 180 --retry-all-errors \
        https://luarocks.github.io/luarocks/releases/luarocks-3.13.0.tar.gz -o /tmp/luarocks.tar.gz; } \
    && echo '245bf6ec560c042cb8948e3d661189292587c5949104677f1eecddc54dbe7e37  /tmp/luarocks.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/luarocks.tar.gz -C /tmp \
    && cd /tmp/luarocks-3.13.0 \
    && ./configure --lua-version=5.5 --with-lua=/usr/local \
    && make -j2 && make install \
    && lua -v \
    && luarocks --version \
    && rm -rf /tmp/lua.tar.gz /tmp/lua-5.5.1 /tmp/luarocks.tar.gz /tmp/luarocks-3.13.0
