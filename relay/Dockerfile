# The relay: one static Go binary and nothing else. Build context is this
# directory (docker build relay/).
#
# Build stage. Runs on the builder's own architecture and cross-compiles, so
# arm64 isn't built under emulation. Pinned by tag and index digest. Bump both
# together, and keep the `go` line in go.mod on the same minor.
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine3.24@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build

WORKDIR /src

# No go.sum: the relay uses the standard library only.
COPY go.mod ./
COPY *.go ./

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /relay .

# Nothing but the binary: no shell, no package manager, nothing to patch
# except by rebuilding. The CA bundle is there in case INTAKE_URL is https.
FROM scratch

LABEL org.opencontainers.image.source="https://github.com/yolorepair/site" \
      org.opencontainers.image.title="yolorepair.com relay" \
      org.opencontainers.image.description="Forwards the site's repair-request form to benchhud-intake. Stores nothing. Listens on :8080"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /relay /relay

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/relay"]
