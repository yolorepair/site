# nginx stable, rootless, listens on 8080. Pinned by tag and digest (the digest
# is the multi-arch index, so arm64 and amd64 each pull their own). Bump both
# together.
FROM nginxinc/nginx-unprivileged:1.30.6-alpine@sha256:f5116ed4606f3eb015dd4a8e28cb9b351476dc0ad4bc6e9f93b19cec130cdf03

LABEL org.opencontainers.image.source="https://github.com/yolorepair/site" \
      org.opencontainers.image.title="yolorepair.com" \
      org.opencontainers.image.description="Static site for YOLO Electronics Repair, served by nginx-unprivileged on :8080"

COPY nginx/security-headers.conf /etc/nginx/snippets/security-headers.conf
COPY nginx/default.conf /etc/nginx/conf.d/default.conf
COPY site/ /usr/share/nginx/html/

EXPOSE 8080
