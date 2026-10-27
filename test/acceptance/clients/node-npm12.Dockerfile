# The second npm client (decision 0034, #294): npm 12 on Node 24 LTS,
# retaining the prior Node line beside the current Node 26 client. The
# base is pinned by digest and moved by Dependabot's docker entry; npm itself
# is pinned here and moved by hand, since Dependabot reads only FROM lines.
# The job builds it as artifactweave-accept-npm12.
FROM node:26.12.1-bookworm-slim@sha256:28147155d47dc31abe0a46e0d12b59b2fb3d0a2dcc5dc65fce76a074180b16fe
RUN npm install --global npm@12.2.1 && npm cache clean --force
