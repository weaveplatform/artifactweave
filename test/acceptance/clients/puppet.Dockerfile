# Client pins: Puppet 8.10.0 on Ruby 3.3.13.
# The Ruby 4 bump in #895 breaks this pinned client's dependency resolution.
FROM ruby:3.3.13-slim@sha256:c6c9935e104f9fbc58b7a5d5a0d8aabe78a91660f9c0313b89e18abf7d0de355
RUN gem install --no-document puppet -v 8.10.0 && puppet --version
ENTRYPOINT ["puppet"]
