# Client pins: opkg 0.10.0.
# source digest and base image digest make real-client results repeatable.
FROM ubuntu:26.10@sha256:262df65c0ec6bf8d9dcc87454f4330a05fae4b97825f45854af3b136666584c2

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential cmake pkg-config libarchive-dev libsolv-dev \
    libcurl4-gnutls-dev libssl-dev libgpgme-dev ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://downloads.yoctoproject.org/releases/opkg/opkg-0.10.0.tar.gz \
      -o /tmp/opkg.tar.gz \
    && echo '4d099e010a5d262b34184fee4766bc60a8c7439741fb210aa11d51704afd04d1  /tmp/opkg.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/opkg.tar.gz -C /tmp \
    && cmake -S /tmp/opkg-0.10.0 -B /tmp/opkg-build \
         -DCMAKE_INSTALL_PREFIX=/usr/local -DUSE_SOLVER_LIBSOLV=ON \
         -DWITH_CURL=ON -DWITH_GPGME=ON \
    && cmake --build /tmp/opkg-build -j2 \
    && cmake --install /tmp/opkg-build \
    && rm -rf /tmp/opkg.tar.gz /tmp/opkg-0.10.0 /tmp/opkg-build

RUN mkdir -p /etc/opkg /var/lib/opkg/lists /var/lib/opkg/info

# Retain the installed client identity in the native image build log.
RUN opkg --version
