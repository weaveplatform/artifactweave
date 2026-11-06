# Client pins: Node 24.22.2, npm 12.2.1.
# The second npm client (decision 0034, #294): npm 12 on Node 24 LTS, which
# keeps the prior Node line beside the newest Node that node.Dockerfile
# tracks. The base is pinned by digest and moved by Dependabot's docker
# entry; npm itself is pinned here and moved by hand, since Dependabot reads
# only FROM lines. It has a directory of its own because Dependabot's entry
# for it keeps it below Node 25, which an entry shared with node.Dockerfile
# would do to that image too. While they shared one, #934 moved this image
# to Node 26 (#1119). The job builds it as artifactweave-accept-npm12.
FROM node:24.22.2-bookworm-slim@sha256:01d566e8d94d8c619591d78652babfccd3c6992a3dffa9ed5ab39e11cf6c70e9
RUN npm install --global npm@12.2.1 && npm cache clean --force

# Retain the installed client identity in the native image build log.
RUN node --version && npm --version
