# Client pins: Node 26.13.0, bundled npm.
# The current npm client for the acceptance features (decision 0015): the
# newest Node with its bundled npm. Pinned by digest; Dependabot's docker
# entry moves it. The job builds it as artifactweave-accept-node and runs it
# with --network host, so npm reaches the binary on 127.0.0.1.
FROM node:26.13.1-bookworm-slim@sha256:5df79989ed685e09f0a98019db4cebfb22fb9df1a53c0161664a011f7ff6b0f1
