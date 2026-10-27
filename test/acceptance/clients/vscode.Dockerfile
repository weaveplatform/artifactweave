# Official VS Code 1.143.1 native gallery client, pinned to Microsoft's release SHA-256.
# The signed gallery install is qualified on current Ubuntu 26.10.
FROM ubuntu:26.10@sha256:93e29beff5257b2cdf1c024d831f6d2997e53655a653ff76a811fc9e6564a719

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl python3 libasound2t64 libgbm1 libgtk-3-0 libnss3 libxss1 libxtst6 libsecret-1-0 \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) archive=code-stable-x64-1792691793.tar.gz; sha256=48f658052e54e020a1c1be7b37ac0f9ae39f07e8ccda805c63f053cfda72b2be ;; \
      arm64) archive=code-stable-arm64-1792691733.tar.gz; sha256=e84bf20d112fdb73c29b85b317ba50d5a48bea05c0711509669c8466639e9eb6 ;; \
      *) exit 1 ;; \
    esac \
    && curl -fsSL --retry 3 \
      "https://vscode.download.prss.microsoft.com/dbazure/download/stable/e646868227c3e3a9e3f24277c8c7220be42c0be7/$archive" \
      -o /tmp/vscode.tar.gz \
    && echo "$sha256  /tmp/vscode.tar.gz" | sha256sum -c - \
    && mkdir -p /opt/vscode \
    && tar -xzf /tmp/vscode.tar.gz -C /opt/vscode --strip-components=1 \
    && chmod a+w /opt/vscode/resources/app/product.json \
    && rm /tmp/vscode.tar.gz

COPY vscode-install.sh /usr/local/bin/vscode-install
RUN chmod +x /usr/local/bin/vscode-install
