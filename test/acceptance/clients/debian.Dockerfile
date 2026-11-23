# Client pins: APT bundled with Ubuntu 26.10 (immutable image).
FROM ubuntu:26.10@sha256:1c2a84ae3f2a24aeb97cded5b41f0211b441662f1a188cbd2e139e7625f6f1d6

# Retain the installed client identity in the native image build log.
RUN apt-get --version
