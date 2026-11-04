# Client pins: APT bundled with Ubuntu 26.10 (immutable image).
FROM ubuntu:26.10@sha256:262df65c0ec6bf8d9dcc87454f4330a05fae4b97825f45854af3b136666584c2

# Retain the installed client identity in the native image build log.
RUN apt-get --version
