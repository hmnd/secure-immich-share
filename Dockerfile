# syntax=docker/dockerfile:1

# --- web build (Vite: the islands and their manifest → /src/web/build) ---
FROM node:26.12.0-alpine@sha256:6f08a523443ddedf52941874c203475b0ad503e859ad70cb26ab8be9a517dbd5 AS web
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN npm install -g pnpm@12.14.0 && pnpm install --frozen-lockfile
COPY api/ ../api/
COPY web/ ./
RUN pnpm build

# --- go build (web build embedded into internal/webassets/dist) ---
FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS go
WORKDIR /src/server
ENV CGO_ENABLED=0
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=web /src/web/build/ ./internal/webassets/dist/
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
    -ldflags "-s -w -X github.com/hmnd/secure-immich-share/server/internal/buildinfo.Version=${VERSION}" \
    -o /out/secure-immich-share ./cmd/secure-immich-share

# --- app image: exiftool (perl) and ffmpeg run as child processes, so no distroless ---
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS app
RUN apk add --no-cache exiftool=13.55-r0 ffmpeg=8.1.3-r0 \
    && addgroup -S -g 10001 app && adduser -S -D -H -u 10001 -G app app \
    && mkdir /cache && chown app:app /cache
COPY --from=go --chown=root:root --chmod=0755 /out/secure-immich-share /secure-immich-share
USER 10001:10001
ENV CACHE_DIR=/cache LISTEN_ADDR=:8080
EXPOSE 8080
VOLUME /cache
HEALTHCHECK --interval=30s --timeout=5s CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/secure-immich-share"]
CMD ["serve"]
