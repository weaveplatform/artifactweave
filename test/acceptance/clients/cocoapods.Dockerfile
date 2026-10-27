# CocoaPods runs on the current stable Ruby; the official image index is pinned.
FROM ruby:4.0.9-slim@sha256:88f0d8f1f2f7829c8c85c5cfee774f7ac936a7ba460e904e8c873b6a7f96c311

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git rsync \
    && rm -rf /var/lib/apt/lists/* \
    && gem install cocoapods -v 1.17.0 --no-document

ENV COCOAPODS_DISABLE_STATS=true
