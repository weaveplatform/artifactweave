# syntax=docker/dockerfile:1
# Local and acceptance image. Built as `make build` builds the binaries:
# static, GOWORK=off, without the Cloud Storage gRPC modules.
#
# Each base is pinned by the digest of its multi-arch index, and Dependabot
# moves both (.github/dependabot.yml). The runtime base matches
# Dockerfile.release's, and one Dependabot PR moves the two files together.
# The build stage's tag names the full Go release: Dependabot never proposes a
# tag more precise than the current one, so from 1.27-bookworm it could only
# move the digest, and the file would not say which Go it builds with.
# TestUnitDockerfileGoNotBehindToolchain reads that release and fails when it
# is older than the Go the unit tests run on.

FROM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
ENV GOWORK=off CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=devel
ARG COMMIT=unknown
RUN MODULE=$(go list -m) && \
    go build -trimpath -tags disable_grpc_modules \
      -ldflags "-s -w -X ${MODULE}/internal/platform/buildinfo.version=${VERSION} -X ${MODULE}/internal/platform/buildinfo.commit=${COMMIT}" \
      -o /out/artifactweave ./cmd/artifactweave

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/artifactweave /usr/local/bin/artifactweave
# /data holds the SQLite database and fs blobs; mount a volume there.
WORKDIR /data
USER nonroot:nonroot
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/artifactweave"]
CMD ["serve", "--config", "/etc/artifactweave/artifactweave.yaml"]
