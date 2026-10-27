# The second npm client (decision 0034, #294): npm 12 on Node 24 LTS,
# retaining the prior Node line beside the current Node 26 client. The
# base is pinned by digest and moved by Dependabot's docker entry; npm itself
# is pinned here and moved by hand, since Dependabot reads only FROM lines.
# The job builds it as artifactweave-accept-npm12.
FROM node:24.22.1-bookworm-slim@sha256:55fb5f5a527ea2c5c4a95f963e48ef90aa38c7b346054fdc8ee9957a2b55d205
RUN npm install --global npm@12.2.1 && npm cache clean --force
