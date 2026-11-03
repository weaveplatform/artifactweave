# Bower 1.8.14 and JFrog's bower-art-resolver 2.0.10. The base is pinned to
# the same immutable Node 26 image used by the primary npm acceptance client.
FROM node:26.13.0-bookworm-slim@sha256:bc0a2afd3b74ce12222fc8bab99271e66257f021078f5f4a5d687a77d280c1d9
RUN npm install --prefix /opt/bower --no-audit --no-fund --ignore-scripts \
    bower@1.8.14 bower-art-resolver@2.0.10
ENV NODE_PATH=/opt/bower/node_modules
ENTRYPOINT ["/opt/bower/node_modules/.bin/bower"]
