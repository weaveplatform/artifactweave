# Client pins: Dart/pub 3.14.0.
FROM dart:3.14.1@sha256:36176bf7b6400bdc4aa77ba07b733b2eabf22640f71afd1b83eba679a26d5698
COPY dart-entrypoint.sh /usr/local/bin/artifactweave-dart
RUN chmod 0755 /usr/local/bin/artifactweave-dart

# Retain the installed client identity in the native image build log.
RUN dart --version
