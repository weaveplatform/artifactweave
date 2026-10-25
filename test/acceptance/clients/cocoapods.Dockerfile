FROM ruby:3.3.6-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git rsync \
    && rm -rf /var/lib/apt/lists/* \
    && gem install cocoapods -v 1.17.0 --no-document

ENV COCOAPODS_DISABLE_STATS=true
