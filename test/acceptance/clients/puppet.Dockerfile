# Client pins: Puppet 8.10.0 on Ruby 3.3.6.
# The Ruby 4 bump in #895 breaks this pinned client's dependency resolution.
FROM ruby:3.3.6-slim@sha256:b210597cc7d05e19edf9c8e7935dcf1554886905dcdad0475cfe63a654147f7a
RUN gem install --no-document puppet -v 8.10.0 && puppet --version
ENTRYPOINT ["puppet"]
