# Client pins: Bower 1.8.14; bower-art-resolver 2.0.10; Node 26.13.1.
# The immutable Node image is updated alongside the primary npm acceptance client.
FROM node:26.13.1-bookworm-slim@sha256:5df79989ed685e09f0a98019db4cebfb22fb9df1a53c0161664a011f7ff6b0f1
RUN apt-get update && apt-get install -y --no-install-recommends git openssh-client ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN npm install --prefix /opt/bower --no-audit --no-fund --ignore-scripts \
    bower@1.8.14 bower-art-resolver@2.0.10
ENV NODE_PATH=/opt/bower/node_modules
ENTRYPOINT ["/opt/bower/node_modules/.bin/bower"]

# Retain the installed client identity in the native image build log.
RUN /opt/bower/node_modules/.bin/bower --version \
    && node -e 'console.log("bower-art-resolver", require("/opt/bower/node_modules/bower-art-resolver/package.json").version)'
