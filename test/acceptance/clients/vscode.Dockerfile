# Client pins: VS Code official archive commit `261c84cb1eddf11235f9e2c921425e9663fbe556`; Ubuntu 26.10.
# Native gallery installation is exercised on the pinned Ubuntu image.
FROM ubuntu:26.10@sha256:262df65c0ec6bf8d9dcc87454f4330a05fae4b97825f45854af3b136666584c2

# The bundled vsce-sign verifier loads libssl.so.3 at runtime; Ubuntu 26.10
# otherwise provides only libssl.so.4 and fails closed before verification.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl python3 libasound2t64 libgbm1 libgtk-3-0 libnss3 libssl3t64 libxss1 libxtst6 libsecret-1-0 \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) archive=code-stable-x64-1793549455.tar.gz; sha256=59d8d6f9dc2431353c453cf6a4da0faae3993e332007ab387f526332eef93fe4 ;; \
      arm64) archive=code-stable-arm64-1793549425.tar.gz; sha256=93882091a3bce93441cb8671ca43ed20d666c4224d272dee693556055cbb6862 ;; \
      *) exit 1 ;; \
    esac \
    && curl -fsSL --retry 3 \
      "https://vscode.download.prss.microsoft.com/dbazure/download/stable/261c84cb1eddf11235f9e2c921425e9663fbe556/$archive" \
      -o /tmp/vscode.tar.gz \
    && echo "$sha256  /tmp/vscode.tar.gz" | sha256sum -c - \
    && mkdir -p /opt/vscode \
    && tar -xzf /tmp/vscode.tar.gz -C /opt/vscode --strip-components=1 \
    && chmod a+w /opt/vscode/resources/app/product.json \
    && rm /tmp/vscode.tar.gz

COPY vscode-install.sh /usr/local/bin/vscode-install
RUN chmod +x /usr/local/bin/vscode-install
