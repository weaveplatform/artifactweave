# Client pins: Node 26.13.1, bundled npm.
# The current npm client for the acceptance features (decision 0015): the
# newest Node with its bundled npm. Pinned by digest; Dependabot's docker
# entry moves it. The job builds it as artifactweave-accept-node and runs it
# with --network host, so npm reaches the binary on 127.0.0.1.
FROM node:26.13.1-bookworm-slim@sha256:0042e17d232a87a2cd485be1da7900fca4d10a4ad11b29e9285da1d2e54798c9
