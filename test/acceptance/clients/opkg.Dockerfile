# opkg 0.7.0, the version in JFrog's current setup guide. The upstream
# source digest and base image digest make real-client results repeatable.
FROM ubuntu:24.04@sha256:68ee22004a9109b44973313b02ff7b1de250f1de0f5ecb628d73f579aaff33ac

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential libtool autoconf automake pkg-config libarchive-dev \
    libcurl4-gnutls-dev libssl-dev libgpgme-dev ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://downloads.yoctoproject.org/releases/opkg/opkg-0.7.0.tar.gz \
      -o /tmp/opkg.tar.gz \
    && echo 'd973fd0f1568f58f87d6aecd9aa95e3e1f60214a45cee26704bf8fe757c54567  /tmp/opkg.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/opkg.tar.gz -C /tmp \
    && cd /tmp/opkg-0.7.0 \
    && ./configure --with-static-libopkg --disable-shared --enable-gpg --enable-curl \
         --prefix=/usr/local --sysconfdir=/etc \
    && make -j2 \
    && make install \
    && rm -rf /tmp/opkg.tar.gz /tmp/opkg-0.7.0

RUN mkdir -p /etc/opkg /var/lib/opkg/lists /var/lib/opkg/info
