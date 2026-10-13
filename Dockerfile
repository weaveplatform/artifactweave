# syntax=docker/dockerfile:1
# Local and acceptance image. Built as `make build` builds the binaries:
# static, GOWORK=off, without the Cloud Storage gRPC modules. Base images
# are pinned by digest; bump them deliberately.

FROM golang:1.27-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
WORKDIR /src
ENV GOWORK=off CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=devel
ARG COMMIT=unknown
RUN MODULE=$(go list -m) && \
    go build -trimpath -tags disable_grpc_modules \
      -ldflags "-s -w -X ${MODULE}/internal/buildinfo.version=${VERSION} -X ${MODULE}/internal/buildinfo.commit=${COMMIT}" \
      -o /out/artifactweave ./cmd/artifactweave

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/artifactweave /usr/local/bin/artifactweave
# /data holds the SQLite database and fs blobs; mount a volume there.
WORKDIR /data
USER nonroot:nonroot
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/artifactweave"]
CMD ["serve", "--config", "/etc/artifactweave/artifactweave.yaml"]
