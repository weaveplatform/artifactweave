# Public Puppet client 8.10.0; upstream commercial Core versions are separate.
FROM ruby:4.0.0-slim@sha256:fe89c3e461b5258352ed5cbe0355fe239a0c7d0ca6c4d9fa22537b98d4432a6c
RUN gem install --no-document puppet -v 8.10.0 && puppet --version
ENTRYPOINT ["puppet"]
