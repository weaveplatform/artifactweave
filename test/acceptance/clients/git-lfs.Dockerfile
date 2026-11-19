# Client pins: Git LFS 3.8.1.
FROM ubuntu:26.10@sha256:c4ca31b6e93261c5dd773826f789738a49e414ca04fc094b1ccd0f91e07329a2

COPY ubuntu-mirrors.sh /usr/local/bin/ubuntu-mirrors
RUN ubuntu-mirrors

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) archive=git-lfs-linux-amd64-v3.8.1.tar.gz; sha256=e72cf723a4a660438862282e8aecf3885fc8123e88bfcad534cf6b941a354b75 ;; \
      arm64) archive=git-lfs-linux-arm64-v3.8.1.tar.gz; sha256=67895dd77b63329796bc5efdd47b57f218e1f7b98717ebaf936a8238dbcaec0f ;; \
      *) exit 1 ;; \
    esac \
    && curl -fsSL --retry 3 "https://github.com/git-lfs/git-lfs/releases/download/v3.8.1/$archive" -o /tmp/git-lfs.tar.gz \
    && echo "$sha256  /tmp/git-lfs.tar.gz" | sha256sum -c - \
    && mkdir -p /opt/git-lfs \
    && tar -xzf /tmp/git-lfs.tar.gz -C /opt/git-lfs --strip-components=1 \
    && install /opt/git-lfs/git-lfs /usr/local/bin/git-lfs \
    && rm -rf /tmp/git-lfs.tar.gz /opt/git-lfs \
    && git lfs version
