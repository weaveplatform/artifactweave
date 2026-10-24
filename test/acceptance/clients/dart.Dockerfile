# Official Dart SDK 3.10.0. The digest pins the native client used by @pub.
FROM dart:3.10.0@sha256:78ec6a5fd1834ad0f5816e314ef8ca2abbf49c1fb8b9019ee5ba69890435110f
COPY dart-entrypoint.sh /usr/local/bin/artifactweave-dart
RUN chmod 0755 /usr/local/bin/artifactweave-dart
