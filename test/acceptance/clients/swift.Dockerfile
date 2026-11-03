# Client pins: Swift/SwiftPM 6.4.2 pinned Linux client.
# package-registry client publishes, resolves and downloads in acceptance.
FROM swift:6.4.2-jammy@sha256:e86bcb3d1aa6e2bcd54251bad4b82b1f9d825757081c058840f7483c0d164744

# SwiftPM's registry publish command shells out to `zip` when creating the
# source archive. Keep the real client path intact in acceptance.
RUN apt-get update \
    && apt-get install -y --no-install-recommends zip \
    && rm -rf /var/lib/apt/lists/*
