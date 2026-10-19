# The second npm client (decision 0034, #294): npm 12 on the same Node 24 LTS
# image as node.Dockerfile, because no Node release bundles npm 12 yet. The
# base is pinned by digest and moved by Dependabot's docker entry; npm itself
# is pinned here and moved by hand, since Dependabot reads only FROM lines.
# The job builds it as artifactweave-accept-npm12.
FROM node:24.22.0-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5
RUN npm install --global npm@12.2.1 && npm cache clean --force
