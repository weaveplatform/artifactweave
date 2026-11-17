# Client pins: Swift/SwiftPM 6.4.2 pinned Linux client.
# SwiftPM is pinned to the multi-platform image index. Its native
# package-registry client publishes, resolves and downloads in acceptance.
FROM swift:6.4.2-jammy@sha256:18a1cdd3e55ee32b0b3e1a15f12a4e1320b67544e542ed96c907629e5e434144

COPY ubuntu-mirrors.sh /usr/local/bin/ubuntu-mirrors
RUN ubuntu-mirrors

# SwiftPM's registry publish command shells out to `zip` when creating the
# source archive. Keep the real client path intact in acceptance.
RUN apt-get update \
    && apt-get install -y --no-install-recommends zip \
    && rm -rf /var/lib/apt/lists/*

# Retain the installed client identity in the native image build log.
RUN swift --version && swift package --version
