# Client pins: Node 26.13.0, bundled npm.
# The current npm client for the acceptance features (decision 0015): the
# newest Node with its bundled npm. Pinned by digest; Dependabot's docker
# entry moves it. The job builds it as artifactweave-accept-node and runs it
# with --network host, so npm reaches the binary on 127.0.0.1.
FROM node:26.13.0-bookworm-slim@sha256:bc0a2afd3b74ce12222fc8bab99271e66257f021078f5f4a5d687a77d280c1d9
