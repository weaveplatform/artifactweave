# SwiftPM 6.0.3 is pinned to the multi-platform image index. Its native
# package-registry client publishes, resolves and downloads in acceptance.
FROM swift:6.0.3-jammy@sha256:a159706c0c52c1025998a87bc2ab3ceba71416a00110b31cc2ab08d9184341c9

# SwiftPM's registry publish command shells out to `zip` when creating the
# source archive. Keep the real client path intact in acceptance.
RUN apt-get update \
    && apt-get install -y --no-install-recommends zip \
    && rm -rf /var/lib/apt/lists/*
