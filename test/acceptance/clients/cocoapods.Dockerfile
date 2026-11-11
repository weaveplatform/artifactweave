# Client pins: CocoaPods 1.17.0; Ruby 4.0.9.
FROM ruby:4.0.9-slim@sha256:d8cb2c0714bdaba0f318af897a01d2dbde3cfa8e62d47e99cea55feb13c0e90d

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git rsync unzip \
    && rm -rf /var/lib/apt/lists/* \
    && gem install cocoapods -v 1.17.0 --no-document

ENV COCOAPODS_DISABLE_STATS=true

# Retain the installed client identity in the native image build log.
RUN pod --allow-root --version
