# Bower 1.8.14 and JFrog's bower-art-resolver 2.0.10. The base is pinned to
# the same immutable Node 24 image used by the npm acceptance client.
FROM node:24.22.0-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5
RUN npm install --prefix /opt/bower --no-audit --no-fund --ignore-scripts \
    bower@1.8.14 bower-art-resolver@2.0.10
ENV NODE_PATH=/opt/bower/node_modules
ENTRYPOINT ["/opt/bower/node_modules/.bin/bower"]
