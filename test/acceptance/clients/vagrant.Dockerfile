# Client pins: Vagrant 2.4.9 (Linux amd64).
# Build with --platform=linux/amd64, including on an ARM Docker Desktop host.
FROM ubuntu:26.10@sha256:1c2a84ae3f2a24aeb97cded5b41f0211b441662f1a188cbd2e139e7625f6f1d6

COPY ubuntu-mirrors.sh /usr/local/bin/ubuntu-mirrors
RUN ubuntu-mirrors

# When releases.hashicorp.com does not answer, the .deb comes from HashiCorp's
# apt repository, whose pool holds the same file under the same name (#1322).
COPY fetch-pinned.sh /usr/local/bin/fetch-pinned
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && fetch-pinned 42d830d05e6e9e647f8a5c854d9f5295e319dac74edd37d54b97b4f29cbbd70d /tmp/vagrant.deb \
      https://releases.hashicorp.com/vagrant/2.4.9/vagrant_2.4.9-1_amd64.deb \
      https://apt.releases.hashicorp.com/pool/amd64/main/vagrant_2.4.9-1_amd64.deb \
    && apt-get install -y --no-install-recommends /tmp/vagrant.deb \
    && rm -rf /var/lib/apt/lists/* /tmp/vagrant.deb \
    && vagrant --version | grep -Fx 'Vagrant 2.4.9'

ENV CHECKPOINT_DISABLE=1 VAGRANT_CHECKPOINT_DISABLE=1
