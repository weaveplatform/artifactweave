# Eclipse Platform 4.41 (2026-08-28), with the Equinox headless p2 director.
# The archive is pinned to Eclipse's published SHA-512 for each architecture.
FROM ubuntu:24.10@sha256:cdf755952ed117f6126ff4e65810bf93767d4c38f5c7185b50ec1f1078b464cc

RUN apt-get update && apt-get install -y --no-install-recommends \
    openjdk-21-jre-headless libgtk-3-0 libxtst6 ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) archive=eclipse-platform-4.41-linux-gtk-x86_64.tar.gz; \
        sha512=dd4e732816280fb596f1c279f355e2e531668f98c896845ab21d268e3cab30233980bfdd9e5de7b5757f7098194f5dfe8a63b43b9e7da506382a60ee9be84048 ;; \
      arm64) archive=eclipse-platform-4.41-linux-gtk-aarch64.tar.gz; \
        sha512=1f735ab3858536cdb5818628798bc445a19406a2ca841624a6f53af1152d4e6ddaf72bd8e917adf48f124169b43b9a8b4f2d4f38b37a5911e6d5509aadeb4036 ;; \
      *) exit 1 ;; \
    esac \
    && curl -fsSL --retry 3 \
      "https://archive.eclipse.org/eclipse/downloads/drops4/R-4.41-202608281142/$archive" \
      -o /tmp/eclipse-platform.tar.gz \
    && echo "$sha512  /tmp/eclipse-platform.tar.gz" | sha512sum -c - \
    && mkdir -p /opt \
    && tar -xzf /tmp/eclipse-platform.tar.gz -C /opt \
    && rm /tmp/eclipse-platform.tar.gz

ENV PATH="/opt/eclipse:${PATH}"

COPY p2-director.sh /usr/local/bin/p2-director
RUN chmod 0755 /usr/local/bin/p2-director
