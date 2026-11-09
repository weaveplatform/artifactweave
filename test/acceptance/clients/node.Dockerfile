# Client pins: Node 26.13.0, bundled npm.
# The current npm client for the acceptance features (decision 0015): the
# newest Node with its bundled npm. Pinned by digest; Dependabot's docker
# entry moves it. The job builds it as artifactweave-accept-node and runs it
# with --network host, so npm reaches the binary on 127.0.0.1.
FROM node:26.13.0-bookworm-slim@sha256:add5c17c14752c54d2f3d519ec46a3815684d41c33f198bcb09fb998f7dcbf68
