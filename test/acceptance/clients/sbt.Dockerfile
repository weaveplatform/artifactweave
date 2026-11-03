# sbt 2.0.12, verified against the release asset's published SHA-256.
# Keep tool bootstrap artifacts apart from the fresh dependency caches used
# by each recipe invocation; no project artifact is baked into this image.
FROM maven:3.9-eclipse-temurin-26@sha256:b2c1ad85954592f9928e84327c65201f308ad9b5d8ed7d5b823717c97bf23fbb

RUN curl -fsSL --retry 3 --max-time 300 \
      https://github.com/sbt/sbt/releases/download/v2.0.12/sbt-2.0.12.tgz -o /tmp/sbt.tgz \
    && echo 'a797e1d940260c9d6e8a0a434ff8fc3247c1b711d2cf302bae62ae243293fe55  /tmp/sbt.tgz' | sha256sum -c - \
    && tar -xzf /tmp/sbt.tgz -C /opt \
    && rm /tmp/sbt.tgz \
    && mkdir -p /tmp/bootstrap/project \
    && printf 'sbt.version=2.0.12\n' > /tmp/bootstrap/project/build.properties \
    && printf 'name := "bootstrap"\n' > /tmp/bootstrap/build.sbt \
    && cd /tmp/bootstrap \
    && COURSIER_CACHE=/opt/sbt-bootstrap-cache /opt/sbt/bin/sbt --server -batch -Dsbt.boot.directory=/opt/sbt-boot about \
    && chmod -R a+rwX /opt/sbt-boot \
    && chmod -R a+rX /opt/sbt-bootstrap-cache \
    && rm -rf /tmp/bootstrap

ENV PATH="/opt/sbt/bin:${PATH}"
