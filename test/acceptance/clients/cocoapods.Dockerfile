FROM ruby:3.4.11-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git rsync \
    && rm -rf /var/lib/apt/lists/* \
    && gem install cocoapods -v 1.17.0 --no-document

ENV COCOAPODS_DISABLE_STATS=true
