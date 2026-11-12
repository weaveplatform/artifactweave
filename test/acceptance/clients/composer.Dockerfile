# Client pins: Composer 2.11.1; Composer 1.10.28 local compatibility.
# The legacy Composer lane verifies local compatibility during JFrog's support window.
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
# When getcomposer.org does not answer, each phar comes from the copy Composer
# attaches to its GitHub release, the same bytes (#1322).
COPY fetch-pinned.sh /usr/local/bin/fetch-pinned
RUN apk add --no-cache php83 php83-phar php83-mbstring php83-openssl php83-curl php83-zip php83-iconv curl unzip ca-certificates && \
    fetch-pinned 6f1b891e1d4927d62740276c540fb490e47689e19e102c49e8da849794882f8c /usr/local/bin/composer2.phar \
      https://getcomposer.org/download/2.11.1/composer.phar \
      https://github.com/composer/composer/releases/download/2.11.1/composer.phar && \
    fetch-pinned 0915af36eb01e3f0e16cd309adff7051832b9ef014e38371756804b20425cd5a /usr/local/bin/composer1.phar \
      https://getcomposer.org/download/1.10.28/composer.phar \
      https://github.com/composer/composer/releases/download/1.10.28/composer.phar && \
    printf '#!/bin/sh\nexec php83 /usr/local/bin/composer2.phar "$@"\n' > /usr/local/bin/composer && \
    printf '#!/bin/sh\nexec php83 /usr/local/bin/composer1.phar "$@"\n' > /usr/local/bin/composer1 && \
    chmod +x /usr/local/bin/composer /usr/local/bin/composer1

# Retain the installed client identity in the native image build log.
RUN composer --version && composer1 --version
