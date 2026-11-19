# Client pins: APT bundled with Ubuntu 26.10 (immutable image).
FROM ubuntu:26.10@sha256:c4ca31b6e93261c5dd773826f789738a49e414ca04fc094b1ccd0f91e07329a2

# Retain the installed client identity in the native image build log.
RUN apt-get --version
