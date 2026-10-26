# Official VS Code 1.140.0 native gallery client, pinned to Microsoft's release SHA-256.
FROM ubuntu:24.10@sha256:cdf755952ed117f6126ff4e65810bf93767d4c38f5c7185b50ec1f1078b464cc

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl python3 libasound2t64 libgbm1 libgtk-3-0 libnss3 libxss1 libxtst6 libsecret-1-0 \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) archive=code-stable-x64-1790759436.tar.gz; sha256=d32031e9e213d59532af3cf32fcb8b357a1cdd10417967b4f5b5ba30436dc0dc ;; \
      arm64) archive=code-stable-arm64-1790759311.tar.gz; sha256=9609a7655c4bc2a101b343ff22aa91f6678b46d9565b914c0608bd7f2e577fa6 ;; \
      *) exit 1 ;; \
    esac \
    && curl -fsSL --retry 3 \
      "https://vscode.download.prss.microsoft.com/dbazure/download/stable/07f806f999227108933c2e30515b26eecc1fda74/$archive" \
      -o /tmp/vscode.tar.gz \
    && echo "$sha256  /tmp/vscode.tar.gz" | sha256sum -c - \
    && mkdir -p /opt/vscode \
    && tar -xzf /tmp/vscode.tar.gz -C /opt/vscode --strip-components=1 \
    && chmod a+w /opt/vscode/resources/app/product.json \
    && rm /tmp/vscode.tar.gz

COPY vscode-install.sh /usr/local/bin/vscode-install
RUN chmod +x /usr/local/bin/vscode-install
