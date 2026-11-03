# Client pins: apk-tools bundled with Alpine 3.24 (immutable image).
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a

# Retain the installed client identity in the native image build log.
RUN apk --version
