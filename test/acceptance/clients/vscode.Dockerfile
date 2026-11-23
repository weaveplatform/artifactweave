# Client pins: VS Code official archive commit `261c84cb1eddf11235f9e2c921425e9663fbe556`; Ubuntu 26.10.
# Native gallery installation is exercised on the pinned Ubuntu image.
FROM ubuntu:26.10@sha256:1c2a84ae3f2a24aeb97cded5b41f0211b441662f1a188cbd2e139e7625f6f1d6

COPY ubuntu-mirrors.sh /usr/local/bin/ubuntu-mirrors
RUN ubuntu-mirrors

# The bundled vsce-sign verifier loads libssl.so.3 at runtime; Ubuntu 26.10
# otherwise provides only libssl.so.4 and fails closed before verification.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl python3 libasound2t64 libgbm1 libgtk-3-0 libnss3 libssl3t64 libxss1 libxtst6 libsecret-1-0 \
    && rm -rf /var/lib/apt/lists/*

# Microsoft publishes the archive on one host, PRSS's
# vscode.download.prss.microsoft.com. update.code.visualstudio.com, whose API
# names the archive and its SHA-256, only redirects there, and the China CDN
# (vscode.cdn.azure.cn) and the old Azure CDN (az764295.vo.msecnd.net) no
# longer resolve. PRSS's traffic manager hands the name to Akamai or to
# Fastly, so fetch-pinned also tries the URL through each network by name:
# one of them cutting the transfer short, as on #1592's run, no longer fails
# the build (#1603). Both answered with the pinned SHA-256 for both archives
# when #1603 checked them. When PRSS itself is down every source fails, which
# the maintainer accepts (#1322).
COPY fetch-pinned.sh /usr/local/bin/fetch-pinned
ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) archive=code-stable-x64-1793549455.tar.gz; sha256=59d8d6f9dc2431353c453cf6a4da0faae3993e332007ab387f526332eef93fe4 ;; \
      arm64) archive=code-stable-arm64-1793549425.tar.gz; sha256=93882091a3bce93441cb8671ca43ed20d666c4224d272dee693556055cbb6862 ;; \
      *) exit 1 ;; \
    esac \
    && url="https://vscode.download.prss.microsoft.com/dbazure/download/stable/261c84cb1eddf11235f9e2c921425e9663fbe556/$archive" \
    && fetch-pinned "$sha256" /tmp/vscode.tar.gz "$url" \
      "$url via download.prss.microsoft.com.akamaized.net" \
      "$url via fg.microsoft.map.fastly.net" \
    && mkdir -p /opt/vscode \
    && tar -xzf /tmp/vscode.tar.gz -C /opt/vscode --strip-components=1 \
    && chmod a+w /opt/vscode/resources/app/product.json \
    && rm /tmp/vscode.tar.gz

COPY vscode-install.sh /usr/local/bin/vscode-install
RUN chmod +x /usr/local/bin/vscode-install

# Retain the installed client identity in the native image build log.
RUN /opt/vscode/bin/code --no-sandbox --user-data-dir /tmp/vscode-version --version \
    && rm -rf /tmp/vscode-version
