set dotenv-load := true

default:
    @just --list

# all codegen (generated code is committed)
gen: gen-api gen-templ

# API contract (api/openapi.json) from the Go handlers, then the web client from it
gen-api:
    cd server && mise exec -- go run ./cmd/openapi
    pnpm -C web gen:api

# Go code for the page templates (server/internal/pages/*.templ)
gen-templ:
    cd server && mise exec -- go tool templ generate -path internal/pages

# fails when `just gen` changes any generated file (CI drift check)
check-gen:
    #!/usr/bin/env bash
    set -euo pipefail
    snapshot() { find api web/src/lib/api/generated server/internal/pages -type f -print0 | sort -z | xargs -0 sha256sum; }
    before=$(snapshot)
    just gen
    if [ "$before" != "$(snapshot)" ]; then
        echo "generated files are out of date; run 'just gen' and commit:" >&2
        diff <(echo "$before") <(snapshot) >&2 || true
        exit 1
    fi

# Go tests (sanitizer tests need exiftool, ffmpeg, ffprobe and the exif-py samples)
test: check-tools fetch-samples
    cd server && mise exec -- go test -race ./...

check-tools:
    #!/usr/bin/env bash
    set -euo pipefail
    for tool in exiftool ffmpeg ffprobe; do
        command -v "$tool" >/dev/null || { echo "$tool not found on PATH" >&2; exit 1; }
    done

# real camera samples from exif-py (mixed licences, so fetched, not committed)
fetch-samples:
    #!/usr/bin/env bash
    set -euo pipefail
    rev=a69bf74770caf6b333221658f5092ed69f99faac
    dir=server/internal/media/sanitize/testdata/exif-py
    [ -f "$dir/.rev" ] && [ "$(cat "$dir/.rev")" = "$rev" ] && exit 0
    rm -rf "$dir" && mkdir -p "$dir"
    curl -fsSL "https://codeload.github.com/ianare/exif-py/tar.gz/$rev" \
        | tar xz -C "$dir" --strip-components=3 "exif-py-$rev/tests/resources"
    echo "$rev" > "$dir/.rev"

lint:
    cd server && mise exec -- golangci-lint run

vuln:
    cd server && mise exec -- go tool govulncheck ./...

web-install:
    pnpm -C web install

web-check:
    pnpm -C web check

# oxlint (primary) + slim ESLint for Svelte template rules + prettier check
web-lint:
    pnpm -C web lint

web-build:
    pnpm -C web build

# web build (islands + Vite manifest) → server/internal/webassets/dist (embedded by the server)
web-embed: web-build
    find server/internal/webassets/dist -mindepth 1 ! -name .gitignore -delete
    cp -r web/build/. server/internal/webassets/dist/

# full build: web build → embedded assets → go binary
build: web-embed
    cd server && mise exec -- go build -trimpath -o ../bin/secure-immich-share ./cmd/secure-immich-share

dev:
    cd server && mise exec -- go run ./cmd/secure-immich-share serve

# the server with the islands from `pnpm -C web dev` (hot reload); browse Vite's port
dev-vite:
    cd server && VITE_DEV=true mise exec -- go run ./cmd/secure-immich-share serve

# fake Immich with fixtures on :2283 (for `just dev` without an Immich server)
fake-immich:
    cd server && mise exec -- go run ./tests/cmd/fake-immich

# encrypted link for an Immich share URL, key or slug (needs LINK_KEYS)
mint *args:
    cd server && mise exec -- go run ./cmd/secure-immich-share mint {{args}}

# Playwright e2e
e2e:
    cd e2e && pnpm install --silent
    cd e2e && pnpm exec playwright test
