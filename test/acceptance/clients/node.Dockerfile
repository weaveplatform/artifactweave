# The current npm client for the acceptance features (decision 0015): Node 26
# with its bundled npm. Pinned by digest; Dependabot's docker entry moves it.
# The job builds it as artifactweave-accept-node and runs it with
# --network host, so npm reaches the binary on 127.0.0.1.
FROM node:26.12.1-bookworm-slim@sha256:28147155d47dc31abe0a46e0d12b59b2fb3d0a2dcc5dc65fce76a074180b16fe
