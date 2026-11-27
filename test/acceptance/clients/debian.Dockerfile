# Client pins: APT bundled with Ubuntu 26.10 (immutable image).
FROM ubuntu:26.10@sha256:29ac5e68db830ab61b7d77c1ffdb78c73f0631de88a5d77bf4399868f64f38dd

# Retain the installed client identity in the native image build log.
RUN apt-get --version
