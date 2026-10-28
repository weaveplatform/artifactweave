# Public Puppet client 8.10.0; upstream commercial Core versions are separate.
FROM ruby:3.3.6-slim@sha256:b210597cc7d05e19edf9c8e7935dcf1554886905dcdad0475cfe63a654147f7a
RUN gem install --no-document puppet -v 8.10.0 && puppet --version
ENTRYPOINT ["puppet"]
