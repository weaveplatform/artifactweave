# SwiftPM 6.0.3 is pinned to the multi-platform image index. Its native
# package-registry client publishes, resolves and downloads in acceptance.
FROM swift:6.4.1-jammy@sha256:51729e0f6ed2f477490cd1e1132e5eddbf011b81092f4998fc8d88e5435e33cb

# SwiftPM's registry publish command shells out to `zip` when creating the
# source archive. Keep the real client path intact in acceptance.
RUN apt-get update \
    && apt-get install -y --no-install-recommends zip \
    && rm -rf /var/lib/apt/lists/*
