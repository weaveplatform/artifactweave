# Client pins: Dart/pub 3.13.7.
FROM dart:3.15.0-118.1.beta@sha256:47460f87d4c1a8424dec7a7a0d8fef5312412fc05560349d7058eb4e42b995c0
COPY dart-entrypoint.sh /usr/local/bin/artifactweave-dart
RUN chmod 0755 /usr/local/bin/artifactweave-dart

# Retain the installed client identity in the native image build log.
RUN dart --version
