# Client pins: Vagrant 2.4.9 (Linux amd64).
# Build with --platform=linux/amd64, including on an ARM Docker Desktop host.
FROM ubuntu:26.10@sha256:262df65c0ec6bf8d9dcc87454f4330a05fae4b97825f45854af3b136666584c2

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && curl -fsSL --retry 3 https://releases.hashicorp.com/vagrant/2.4.9/vagrant_2.4.9-1_amd64.deb -o /tmp/vagrant.deb \
    && echo '42d830d05e6e9e647f8a5c854d9f5295e319dac74edd37d54b97b4f29cbbd70d  /tmp/vagrant.deb' | sha256sum -c - \
    && apt-get install -y --no-install-recommends /tmp/vagrant.deb \
    && rm -rf /var/lib/apt/lists/* /tmp/vagrant.deb \
    && vagrant --version | grep -Fx 'Vagrant 2.4.9'

ENV CHECKPOINT_DISABLE=1 VAGRANT_CHECKPOINT_DISABLE=1
