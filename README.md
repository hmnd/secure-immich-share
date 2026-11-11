# secure-immich-share

A public gallery for [Immich](https://immich.app) shared links that keeps Immich itself private. Every file it serves (thumbnails, previews, full-size images, videos, live photo motion parts, downloads and zips) is stripped of location and identifying metadata and checked against an allowlist before it is cached.

Replaces [immich-public-proxy](https://github.com/alangrainger/immich-public-proxy).

## How it works

```
visitor ──HTTPS──> reverse proxy ──> secure-immich-share ──private network──> Immich
```

- Images are stripped losslessly with `exiftool`, keeping pixels, orientation and colour profile. Videos are remuxed with `ffmpeg -c copy` to the first video stream and audio, without metadata or data tracks.
- The verifier reads each output with `exiftool -ee` and rejects any tag not on the allowlist, and any bytes after the end of the image (Pixel motion photos, Samsung trailers).
- RAW, DNG and TIFF originals are never served; Immich's full-size or preview render is used instead. Edited images are served from the full-size render, since originals lack the edits.
- No Immich API key is used; Immich is read through the share key or slug only.
- Pages and JSON contain only allowlisted fields: no filenames, camera, location or people. Dates are local date only (`SHOW_DATES`); descriptions are off by default (`SHOW_DESCRIPTIONS`).
- With `SHOW_DATES`, full-size images and downloads carry the date taken from Immich so phone galleries sort saved files correctly: images get EXIF `DateTimeOriginal` in local time without a time zone, videos the UTC creation time. Nothing else about the date (sub-seconds, offsets, other EXIF dates) is kept. PNGs stay undated.

## Immich setup

For each share:

- Turn on **Allow download** to serve full-size images and downloads, which are stripped from the originals. Immich turns downloads off when **Show metadata** is off, so enable that too. Without them, visitors get only thumbnails, previews and video playback.
- Password-protected shares are supported; unlocks are rate-limited per visitor and per share.

Set Immich's **external domain** (Administration → Settings → Server) to `PUBLIC_URL` so "Copy link" produces working `/share/{key}` and `/s/{slug}` links.

## Link kinds and the threat model

| Link | Path | Enabled by |
|---|---|---|
| Immich key | `/share/{key}` | `IMMICH_KEY_LINKS=true` (default) |
| Immich slug | `/s/{slug}` | `IMMICH_SLUG_LINKS=true` (default) |
| Encrypted token | `/t/{token}` | setting `LINK_KEYS` |

**Key and slug links expose the real Immich share key or slug.** With Show metadata on, anyone who can reach Immich with it gets the full EXIF data, including GPS, and with Allow download, the originals too. Only use these links if no recipient can reach Immich over the internet, LAN or Tailscale. If Immich has a public hostname, block `/share/`, `/s/`, `/api/shared-links` and `?key=` / `?slug=` requests there.

**Token links** encrypt the Immich key with a key only this service holds, so the URL is useless against Immich. To allow only token links, set `IMMICH_KEY_LINKS=false` and `IMMICH_SLUG_LINKS=false`, and mint them with:

```sh
docker compose exec secure-immich-share /secure-immich-share mint https://photos.example.com/share/<key>
```

`LINK_KEYS` is a list of `id:base64key:state` entries (generate keys with `openssl rand -base64 32`). Exactly one key is `active` and mints; `retired` keys still open links. To rotate, add a new active key and retire the old one; remove it to invalidate its links. To revoke a single link, delete or expire the share in Immich; it stops working within `SHARE_TTL`.

Slugs are chosen by the share owner and may be guessable.

## Deployment

```sh
cp .env.example .env   # set IMMICH_URL, PUBLIC_URL, COOKIE_KEY, TRUSTED_PROXIES
docker compose up -d
```

The container listens on `127.0.0.1:8080`; put a reverse proxy in front for HTTPS. The root filesystem is read-only, all capabilities are dropped, and only the cache volume is writable. Attach Immich's Docker network as the compose file's `immich` network and mark it `internal: true`.

**Client IPs.** Rate limits use the client IP. Set `TRUSTED_PROXIES` to your reverse proxy's CIDRs. `X-Forwarded-For` is read right to left, skipping trusted hops, and ignored for connections not from a trusted proxy. The proxy must overwrite a client-supplied `X-Forwarded-For` or append the client address as the last hop.

`/healthz` returns 200 when ready or `degraded` (Immich unreachable; cached shares still work) and 503 while starting or shutting down.

### Configuration

| Variable | Default | |
|---|---|---|
| `IMMICH_URL` | required | e.g. `http://immich-server:2283` |
| `PUBLIC_URL` | required | the only trusted cross-origin origin, also used in Open Graph tags; must be `https` except on localhost/loopback |
| `COOKIE_KEY` | required | base64 of 32 bytes |
| `IMMICH_KEY_LINKS` / `IMMICH_SLUG_LINKS` | `true` / `true` | |
| `LINK_KEYS` | unset | enables `/t/` links |
| `LISTEN_ADDR` | `:8080` | |
| `TRUSTED_PROXIES` | empty | CIDRs |
| `CACHE_DIR` / `CACHE_MAX_BYTES` | `/cache` / `10GiB` | LRU eviction |
| `MAX_SOURCE_BYTES` | `4GiB` | per file fetched from Immich |
| `SANITIZE_CONCURRENCY` / `SANITIZE_QUEUE` / `SANITIZE_TIMEOUT` | CPUs / `64` / `120s` | a full queue returns 503 |
| `SHARE_TTL` | `60s` | revocation latency |
| `SHOW_DESCRIPTIONS` / `SHOW_DATES` / `OG_PREVIEWS` | `false` / `true` / `true` | |
| `PREWARM_VIDEOS` / `PREWARM_IMAGES` / `PREWARM_CONCURRENCY` / `PREWARM_TTL` | `true` / `false` / `1` / `10m` | |
| `FAILED_TTL` | `10m` | retry delay for failed files |
| `ZIP_JOBS` / `ZIP_CONCURRENCY` / `ZIP_TTL` | `2` / half of `SANITIZE_CONCURRENCY` (min `2`) / `15m` | |
| `SHUTDOWN_GRACE` | `30s` | |
| `LOG_LEVEL` | `info` | logs never contain keys, slugs, tokens or Immich URLs |

## Limits

- Metadata inside the compressed stream itself (e.g. H.264/HEVC SEI user data from some drones and action cameras) is not inspected. Phones and Immich's transcodes don't put location there.
- HEIC downloads are the stripped original if it verifies, otherwise Immich's full-size JPEG.

## Development

Tools are pinned in `mise.toml`; `exiftool`, `ffmpeg` and `ffprobe` must be on `PATH`.

```sh
mise install
just web-install
just fetch-samples   # real camera samples for the sanitizer tests
just test            # Go tests (sanitizer corpus, black-box tests against a fake Immich)
just lint && just web-check && just web-lint
just e2e             # Playwright tests in Chromium against the built binary and the fake Immich
just fake-immich     # a fake Immich with fixtures on :2283
just dev             # needs IMMICH_URL, PUBLIC_URL, COOKIE_KEY, CACHE_DIR
```

Pages are rendered server-side with [templ](https://templ.guide) (`server/internal/pages`) and work without JavaScript. Svelte islands (`web/src/islands.ts`) add the lightbox, zip downloads, saving to a phone and the grid layout; `just web-embed` builds them into the binary. For hot reload, run `just dev-vite` and `pnpm -C web dev`, set `PUBLIC_URL=http://localhost:5173` and browse there; Vite proxies everything else to `:8080`.

After changing routes or `server/internal/httpapi/contract`, run `just gen-api` to regenerate `api/openapi.json`. After changing a `.templ` file, run `just gen-templ`.

## License

[AGPL-3.0](LICENSE)
