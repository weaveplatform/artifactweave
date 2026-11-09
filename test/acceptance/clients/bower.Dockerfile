# Client pins: Bower 1.8.14; bower-art-resolver 2.0.10; Node 26.13.0.
# The immutable Node image is updated alongside the primary npm acceptance client.
FROM node:26.13.0-bookworm-slim@sha256:add5c17c14752c54d2f3d519ec46a3815684d41c33f198bcb09fb998f7dcbf68
RUN apt-get update && apt-get install -y --no-install-recommends git openssh-client ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN npm install --prefix /opt/bower --no-audit --no-fund --ignore-scripts \
    bower@1.8.14 bower-art-resolver@2.0.10
ENV NODE_PATH=/opt/bower/node_modules
ENTRYPOINT ["/opt/bower/node_modules/.bin/bower"]

# Retain the installed client identity in the native image build log.
RUN /opt/bower/node_modules/.bin/bower --version \
    && node -e 'console.log("bower-art-resolver", require("/opt/bower/node_modules/bower-art-resolver/package.json").version)'
