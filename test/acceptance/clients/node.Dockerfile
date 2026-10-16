# The npm client for the acceptance features (decision 0015): Node 24 LTS with
# its bundled npm 11. Pinned by digest; Dependabot's docker entry moves it.
# The job builds it as artifactweave-accept-node and runs it with
# --network host, so npm reaches the binary on 127.0.0.1.
FROM node:24.22.0-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5
