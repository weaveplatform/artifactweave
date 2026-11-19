# Client pins: Dart/pub 3.14.0.
FROM dart:3.14.0@sha256:c78970229c8944bd6076caf4e5977ef569ba66750d301a2149f63b41a6725cec
COPY dart-entrypoint.sh /usr/local/bin/artifactweave-dart
RUN chmod 0755 /usr/local/bin/artifactweave-dart

# Retain the installed client identity in the native image build log.
RUN dart --version
