# The fake intake for test/e2e.sh. Never published. test/e2e.sh passes in the
# same pinned Go image relay/Dockerfile builds with, so there's one pin.
ARG GO_IMAGE
# hadolint ignore=DL3006
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /fake-intake .

FROM scratch
COPY --from=build /fake-intake /fake-intake
USER 65532:65532
EXPOSE 8000
ENTRYPOINT ["/fake-intake"]
