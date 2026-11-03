# Client pins: Dart/pub 3.13.7.
FROM dart:3.13.7@sha256:0dacdef11e382f5e3842c2e6198daf5f234e55b1e38ccff1143008823949bcc9
COPY dart-entrypoint.sh /usr/local/bin/artifactweave-dart
RUN chmod 0755 /usr/local/bin/artifactweave-dart
