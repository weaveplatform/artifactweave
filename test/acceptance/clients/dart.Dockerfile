# Client pins: Dart/pub 3.13.7.
FROM dart:3.13.7@sha256:854f3c50b1cbd4a5a54c9fa67d4f5b6702ac49b4a26d21a80e7712a72528b215
COPY dart-entrypoint.sh /usr/local/bin/artifactweave-dart
RUN chmod 0755 /usr/local/bin/artifactweave-dart

# Retain the installed client identity in the native image build log.
RUN dart --version
