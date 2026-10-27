# IntelliJ IDEA Community 2025.2.5: the actual `idea.sh installPlugins` client.
# Both vendor archives are verified with JetBrains' published SHA-256.
FROM ubuntu:26.10@sha256:93e29beff5257b2cdf1c024d831f6d2997e53655a653ff76a811fc9e6564a719

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl libgtk-3-0 libxtst6 \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) archive=ideaIC-2025.2.5.tar.gz; \
        sha256=995c334cc3e143f13467abafef07a1ccf7d06275512bb6f4c91123948786ab7c ;; \
      arm64) archive=ideaIC-2025.2.5-aarch64.tar.gz; \
        sha256=4c57a783f31ee6f2c82d8c43bb5d0334a9afbc8bfb4542e732048c41f61e63a0 ;; \
      *) exit 1 ;; \
    esac \
    && curl -fsSL --retry 3 "https://download.jetbrains.com/idea/$archive" -o /tmp/idea.tar.gz \
    && echo "$sha256  /tmp/idea.tar.gz" | sha256sum -c - \
    && mkdir -p /opt/idea \
    && tar -xzf /tmp/idea.tar.gz -C /opt/idea --strip-components=1 \
    && rm /tmp/idea.tar.gz

ENV PATH="/opt/idea/bin:${PATH}"
