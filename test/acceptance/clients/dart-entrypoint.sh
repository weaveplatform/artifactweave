#!/bin/sh
set -eu

# Dart pub reads the system trust store rather than dart's --root-certs-file
# flag. Trust this scenario's ephemeral TLS front, then run as the CI user so
# its mounted pub cache and generated project files remain removable.
cp /work/tls-front.pem /usr/local/share/ca-certificates/artifactweave-pub-test.crt
update-ca-certificates >/dev/null
exec setpriv --reuid="$AW_CLIENT_UID" --regid="$AW_CLIENT_GID" --clear-groups \
  /usr/lib/dart/bin/dart "$@"
