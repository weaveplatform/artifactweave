# Client pins: Dart/pub 3.14.0.
FROM dart:3.14.0@sha256:9ba97850b3154383a90f67844b09ea77751f7835836d8b2662ad2ffe03f60c8c
COPY dart-entrypoint.sh /usr/local/bin/artifactweave-dart
RUN chmod 0755 /usr/local/bin/artifactweave-dart

# Retain the installed client identity in the native image build log.
RUN dart --version
