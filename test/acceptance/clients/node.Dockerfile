# The npm client for the acceptance features (decision 0015): Node 24 LTS with
# its bundled npm 11. Pinned by digest; Dependabot's docker entry moves it.
# The job builds it as artifactweave-accept-node and runs it with
# --network host, so npm reaches the binary on 127.0.0.1.
FROM node:24.22.1-bookworm-slim@sha256:55fb5f5a527ea2c5c4a95f963e48ef90aa38c7b346054fdc8ee9957a2b55d205
