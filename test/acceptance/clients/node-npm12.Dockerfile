# Client pins: Node 26.13.0, npm 12.2.1.
# retaining the prior Node line beside the current Node 26 client. The
# base is pinned by digest and moved by Dependabot's docker entry; npm itself
# is pinned here and moved by hand, since Dependabot reads only FROM lines.
# The job builds it as artifactweave-accept-npm12.
FROM node:26.13.0-bookworm-slim@sha256:bc0a2afd3b74ce12222fc8bab99271e66257f021078f5f4a5d687a77d280c1d9
RUN npm install --global npm@12.2.1 && npm cache clean --force
