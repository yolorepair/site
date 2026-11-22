# Build stage. Runs on the builder's own architecture: the output is plain
# files, so there's no reason to run npm under emulation for arm64. Pinned by
# tag and index digest, same as nginx below. Bump both together. CI reads its
# Node version from this line.
FROM --platform=$BUILDPLATFORM node:22.23.5-alpine3.24@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a AS build

WORKDIR /src
ENV ASTRO_TELEMETRY_DISABLED=1

# Dependencies first, so this layer survives content-only changes. The dev
# dependencies are the CI validators; the build doesn't need them.
COPY package.json package-lock.json ./
RUN npm ci --omit=dev

COPY astro.config.mjs ./
COPY public/ public/
COPY src/ src/
RUN npm run build

# nginx stable, rootless, listens on 8080. Pinned by tag and digest (the digest
# is the multi-arch index, so arm64 and amd64 each pull their own). Bump both
# together.
FROM nginxinc/nginx-unprivileged:1.30.6-alpine@sha256:f5116ed4606f3eb015dd4a8e28cb9b351476dc0ad4bc6e9f93b19cec130cdf03

LABEL org.opencontainers.image.source="https://github.com/yolorepair/site" \
      org.opencontainers.image.title="yolorepair.com" \
      org.opencontainers.image.description="Static site for YOLO Electronics Repair, built with Astro, served by nginx-unprivileged on :8080"

COPY nginx/security-headers.conf /etc/nginx/snippets/security-headers.conf
COPY nginx/default.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist/ /usr/share/nginx/html/

EXPOSE 8080
