# IntelliJ IDEA 2025.3: the actual `idea.sh installPlugins` client.
# Both vendor archives are verified with JetBrains' published SHA-256.
FROM ubuntu:26.10@sha256:93e29beff5257b2cdf1c024d831f6d2997e53655a653ff76a811fc9e6564a719

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl libgtk-3-0 libxtst6 \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) archive=idea-2025.3.tar.gz; \
        sha256=13f4174ba16c1cef04871cb261433536d002586c269a809392c20ee3f94959f5 ;; \
      arm64) archive=idea-2025.3-aarch64.tar.gz; \
        sha256=07c83a9d219fa8945b6accb7f3284658553e5c812053a97621b8f84bc031e57c ;; \
      *) exit 1 ;; \
    esac \
    && curl -fsSL --retry 3 "https://download.jetbrains.com/idea/$archive" -o /tmp/idea.tar.gz \
    && echo "$sha256  /tmp/idea.tar.gz" | sha256sum -c - \
    && mkdir -p /opt/idea \
    && tar -xzf /tmp/idea.tar.gz -C /opt/idea --strip-components=1 \
    && rm /tmp/idea.tar.gz

ENV PATH="/opt/idea/bin:${PATH}"
