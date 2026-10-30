# SwiftPM 6.4.1 is pinned to the multi-platform image index. Its native
# package-registry client publishes, resolves and downloads in acceptance.
FROM swift:6.4.1-jammy@sha256:64945e64b1e9d16eaf23414f8bcb9b880d12705bb34ad5d23180b04ad6d28e10

# SwiftPM's registry publish command shells out to `zip` when creating the
# source archive. Keep the real client path intact in acceptance.
RUN apt-get update \
    && apt-get install -y --no-install-recommends zip \
    && rm -rf /var/lib/apt/lists/*
