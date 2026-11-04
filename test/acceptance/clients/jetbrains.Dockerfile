# Client pins: IntelliJ IDEA 2026.2.4.
# Both vendor archives are verified with JetBrains' published SHA-256.
FROM ubuntu:26.10@sha256:262df65c0ec6bf8d9dcc87454f4330a05fae4b97825f45854af3b136666584c2

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl libgtk-3-0 libxtst6 \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) archive=idea-2026.2.4.tar.gz; \
        sha256=cc6b29ddb82c7dcc656d5c728cefb2c917ac767eb4b8368f1208732846fbb5bd ;; \
      arm64) archive=idea-2026.2.4-aarch64.tar.gz; \
        sha256=158b97dfbc3a88075fa14fcf5f9f2c17b339f4527e4245b3a33caf18050c2ae2 ;; \
      *) exit 1 ;; \
    esac \
    && curl -fsSL --retry 3 "https://download.jetbrains.com/idea/$archive" -o /tmp/idea.tar.gz \
    && echo "$sha256  /tmp/idea.tar.gz" | sha256sum -c - \
    && mkdir -p /opt/idea \
    && tar -xzf /tmp/idea.tar.gz -C /opt/idea --strip-components=1 \
    && rm /tmp/idea.tar.gz

ENV PATH="/opt/idea/bin:${PATH}"

# Retain the installed client identity in the native image build log.
RUN grep -E '^  "(name|version|buildNumber)"[[:space:]]*:' /opt/idea/product-info.json
