# Latest stable Vagrant 2.4.9; upstream's Linux installer is amd64 only.
# Build with --platform=linux/amd64, including on an ARM Docker Desktop host.
FROM ubuntu:26.10@sha256:93e29beff5257b2cdf1c024d831f6d2997e53655a653ff76a811fc9e6564a719

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && curl -fsSL --retry 3 https://releases.hashicorp.com/vagrant/2.4.9/vagrant_2.4.9-1_amd64.deb -o /tmp/vagrant.deb \
    && echo '42d830d05e6e9e647f8a5c854d9f5295e319dac74edd37d54b97b4f29cbbd70d  /tmp/vagrant.deb' | sha256sum -c - \
    && apt-get install -y --no-install-recommends /tmp/vagrant.deb \
    && rm -rf /var/lib/apt/lists/* /tmp/vagrant.deb \
    && vagrant --version | grep -Fx 'Vagrant 2.4.9'

ENV CHECKPOINT_DISABLE=1 VAGRANT_CHECKPOINT_DISABLE=1
