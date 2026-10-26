# Composer 2.10.3 is the current stable client checked for LAN-02.
# Composer 1.10.28 verifies local compatibility during JFrog's support window.
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache php83 php83-phar php83-mbstring php83-openssl php83-curl php83-zip php83-iconv curl unzip ca-certificates && \
    curl -fsSL https://getcomposer.org/download/2.10.3/composer.phar -o /usr/local/bin/composer2.phar && \
    echo '7a2d379d5b8ffdaa028580ef26494c36d2feef4b178d3dd1473a4dbc5e17c8d6  /usr/local/bin/composer2.phar' | sha256sum -c - && \
    curl -fsSL https://getcomposer.org/download/1.10.28/composer.phar -o /usr/local/bin/composer1.phar && \
    echo '0915af36eb01e3f0e16cd309adff7051832b9ef014e38371756804b20425cd5a  /usr/local/bin/composer1.phar' | sha256sum -c - && \
    printf '#!/bin/sh\nexec php83 /usr/local/bin/composer2.phar "$@"\n' > /usr/local/bin/composer && \
    printf '#!/bin/sh\nexec php83 /usr/local/bin/composer1.phar "$@"\n' > /usr/local/bin/composer1 && \
    chmod +x /usr/local/bin/composer /usr/local/bin/composer1
