# Client pins: Node 24.22.3, npm 12.2.1.
# The second npm client (decision 0034, #294): npm 12 on Node 24 LTS, which
# keeps the prior Node line beside the newest Node that node.Dockerfile
# tracks. The base is pinned by digest and moved by Dependabot's docker
# entry; npm itself is pinned here and moved by hand, since Dependabot reads
# only FROM lines. It has a directory of its own because Dependabot's entry
# for it keeps it below Node 25, which an entry shared with node.Dockerfile
# would do to that image too. While they shared one, #934 moved this image
# to Node 26 (#1119). The job builds it as artifactweave-accept-npm12.
FROM node:24.22.3-bookworm-slim@sha256:873f586f3794f52281bd039bf02c048a0be08fde619338f9941bad1eb6b34530
RUN npm install --global npm@12.2.1 && npm cache clean --force

# Retain the installed client identity in the native image build log.
RUN node --version && npm --version
