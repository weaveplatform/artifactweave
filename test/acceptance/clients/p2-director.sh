#!/bin/sh
set -eu

if [ ! -f /work/p2-truststore ]; then
  keytool -importcert -noprompt -alias artifactweave-acceptance \
    -file /work/p2-front.pem -keystore /work/p2-truststore \
    -storepass changeit >/dev/null
fi

exec /opt/eclipse/eclipse "$@" -vmargs \
  -Djavax.net.ssl.trustStore=/work/p2-truststore \
  -Djavax.net.ssl.trustStorePassword=changeit
