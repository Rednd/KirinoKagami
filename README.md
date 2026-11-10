# KirinoKagami

An osu! beatmap mirror with a cheesegull-compatible API. It scrapes osu! for new and
updated beatmaps, stores them in MySQL/MariaDB, serves searches from an in-memory
index that is rebuilt on a cron schedule, and caches `.osz` downloads on disk.

## API (cheesegull compatible)

| Endpoint | Description |
|---|---|
| `GET /api/s/{setID}` | Beatmapset with `ChildrenBeatmaps` |
| `GET /api/b/{beatmapID}` | Single beatmap |
| `GET /api/md5/{md5}` | Single beatmap by `.osu` checksum |
| `GET /api/search?query=&amount=&offset=&status=&mode=` | Search. `status` / `mode` may repeat (`status=1&status=4`) or be comma-separated. `amount` defaults to 50, max 100 |
| `GET /d/{setID}` | Download `.osz`. Append `n` (`/d/123n`) or `?novideo=1` for the no-video variant |
| `GET /api/status` | DB counts, search index state, scraper stats |

Status values follow cheesegull / osu! API v1: `-2` graveyard, `-1` WIP, `0` pending, `1`
ranked, `2` approved, `3` qualified, `4` loved. Modes: `0` std, `1` taiko, `2` catch, `3` mania.

If `server.fetch_missing` is on, requests for sets that aren't in the database yet are
fetched from osu! straight away and stored.

### Admin endpoints

These need `server.admin_token` set and the header `Authorization: Bearer <token>`.

| Endpoint | Description |
|---|---|
| `POST /admin/search/rebuild` | Rebuild the search index now |
| `POST /admin/sets/{id}/refresh` | Re-fetch one set from osu! now |

## Browser frontend

The frontend is off by default. With `frontend.enabled = true`, `/` serves a web page
for browsing and searching the mirror. It shows each set's cover, title, artist, mapper,
ranked status, difficulties per game mode (coloured by star rating), BPM, length and
favourites.
- **Searching and filtering:** a search box plus status and game-mode filters. A "Load
  more" button below the results fetches the next 48 sets, and the current search is
  kept in the URL so it can be shared.
- **Detail view:** click a set to see its full difficulty list (stars, CS/AR/OD/HP, BPM,
  length, max combo, plays), metadata and tags, plus download, audio-preview and
  osu! website links. Each set has its own shareable URL (`/?set=<id>`).
- **Audio previews:** the play button on a card (or in the detail view) starts that
  set's preview. A player bar at the bottom of the page can pause and resume, skip 5
  seconds back or forward, seek to any point, and close the preview. The same controls
  appear inside the detail view.
- **Theme:** opens in light mode. A button at the top right switches to dark mode, and
  the browser remembers the choice.

It uses the mirror's own `/api/search` and `/api/s/{id}`. Cover images and audio
previews load from osu!'s public asset servers by default (`frontend.cover_base_url`,
`frontend.preview_url`). Set those to empty strings to turn them off. Download buttons
only appear when `downloads.enabled` is on. While the frontend is off, `/` shows a
plain-text info page and only the API is served.

## How it works

**Scraper** (osu! API v2, client credentials, shared rate limit of `api.requests_per_minute`):
- **Discovery**, every `scraper.discover_interval`: walks the "recently updated" (all
  statuses), "recently ranked", "recently loved" and "recently qualified" feeds, newest
  first, until it reaches what it saw on the previous run. This picks up new uploads,
  map updates and status changes.
- **Backfill**, run once and resumable: crawls every beatmapset on osu! so the database
  starts out complete. `backfill_mode = "beatmap_ids"` instead sweeps beatmap IDs 50 at
  a time, if you ever need a more thorough pass. A completed backfill is recorded in the
  `kv` table (`backfill:done:<mode>`). Delete that row to run it again.
- **Refresh**: re-fetches each stored set once it goes stale (`refresh_ranked`,
  `refresh_loved`, `refresh_pending`, `refresh_graveyard`). This updates playcounts,
  favourites, status and difficulties, and deletes sets that no longer exist on osu!.

**Search cache**: every set in the database is loaded into a compact in-memory inverted
index (prefix matching, one token per CJK character, ranked by title > artist > creator > tags,
then favourites). Rendered responses are cached in an LRU. A cron job
(`search.rebuild_cron`, default `*/30 * * * *`) rebuilds the index from the database,
swaps it in atomically, clears the response cache and writes a snapshot to
`search.snapshot_path`, so after a restart search works immediately. Result
metadata is read from the database, and a cached response is at most
`search.result_cache_ttl` (default 30 minutes) old.

**Downloads**: the mirror uses the `osu_session` cookie from a browser that is logged
into osu! (`osu.session_cookie`). It downloads each `.osz` on first request and serves
it from `downloads.cache_dir` afterwards. A file is downloaded again when the set's
`LastUpdate` is newer than the cached file. Concurrent requests for the same set share
one download.

## Setup

1. Create an OAuth application at <https://osu.ppy.sh/home/account/edit#oauth> (any
   callback URL) and note the client ID and secret.
2. `cp config.example.toml config.toml` and fill in `[osu]` (see
   [osu! session cookie](#osu-session-cookie)), `[api]` and `[database]`.
3. Run it, using either option below.

A `Makefile` wraps the Docker commands. `make help` lists them (`make up`, `make up-all`,
`make logs`, `make down-all`, ...).

### Docker, with your own database (default)

Change `database.host`, `user` and `password` in `config.toml` to your server's. A
database on the Docker host itself is reachable as `host.docker.internal`. Make sure MySQL listens on an address the container can reach
and that the user is allowed to connect from the Docker network.

```sh
docker compose up -d --build
```

### Docker, with the bundled MariaDB

On first start MariaDB creates the database `kirinokagami` and the user `kirino` /
password `kirino`. These are the defaults in `config.example.toml`, so the
`[database]` section needs no changes:

```sh
make up-all        # or: docker compose --profile db up -d --build
```

The database isn't published outside the Docker network. To use a different
name, user or password, copy `.env.example` to `.env`, edit it, and set the same
values in `config.toml`. The account is only created when the database volume
is empty. If the volume already exists, either wipe it with `make nuke` (this
deletes the data) or change the account by hand as root. The generated root
password is printed in `make logs-db` on first start.

### Without Docker

```sh
go build -o kirinokagami ./cmd/kirinokagami
./kirinokagami -config config.toml
```

Tables are created automatically on startup.

### Environment overrides

These override the matching config values: `KIRINO_CONFIG` (config path),
`KIRINO_LOG_LEVEL`, `KIRINO_OSU_SESSION_COOKIE`, `KIRINO_API_CLIENT_ID`,
`KIRINO_API_CLIENT_SECRET`, `KIRINO_DB_DSN`, `KIRINO_DB_HOST`, `KIRINO_DB_PASSWORD`, `KIRINO_ADMIN_TOKEN`.

## osu! session cookie

The mirror doesn't log in to osu! itself. Downloads use the session of a browser where
you are already logged in:

1. Log in at <https://osu.ppy.sh> in your browser.
2. Open DevTools (F12), then Storage (Firefox) or Application (Chrome), then Cookies,
   then `https://osu.ppy.sh`.
3. Copy the value of `osu_session` into `osu.session_cookie` in `config.toml` (or set
   `KIRINO_OSU_SESSION_COOKIE`) and restart the mirror (`make restart`).

Treat the cookie like a password. When it expires, or you log out of that browser
session, downloads fail with "session cookie missing or no longer valid" in the log and
`/d/` returns 503. Paste a fresh cookie and restart. Metadata and search keep working
either way. If you only want metadata, set `downloads.enabled = false`.

## Reverse proxy (nginx)

[`nginx/kirinokagami.conf`](nginx/kirinokagami.conf) is a ready-made HTTPS reverse proxy
for the mirror:

- `/api/*` and `/` are proxied with gzip and a per-IP rate limit. `/static/` (the
  frontend's CSS and JavaScript) is proxied with gzip.
- `/d/*` is streamed without buffering. It gets long timeouts, because the first download of
  a set waits on osu!, and a stricter rate limit.
- `/admin/*` is only allowed from the machine itself.
- HTTP redirects to HTTPS, and every other path returns 404.

Copy it to `/etc/nginx/conf.d/`, replace `mirror.example.com` and the certificate paths,
then run `nginx -t && systemctl reload nginx`. When using it, change the port in
`docker-compose.yml` to `"127.0.0.1:62011:62011"` so the mirror can't be reached
directly around nginx.

### Behind Cloudflare

- Set **SSL/TLS** mode to **Full (strict)**. With **Flexible**, Cloudflare connects to
  port 80, nginx redirects that to HTTPS, and every request ends in a redirect loop.
  A Cloudflare Origin Certificate (**SSL/TLS → Origin Server**) works as the server
  certificate.
- Also copy [`nginx/cloudflare-realip.conf`](nginx/cloudflare-realip.conf) to
  `/etc/nginx/conf.d/`. Otherwise every request appears to come from a Cloudflare IP,
  and the per-IP rate limits throttle all users together.
- Cloudflare gives up after 100 seconds. A set's first download can occasionally take
  longer and fail with a 524. The mirror still finishes it in the background, so a retry
  is served from cache.
- Bot Fight Mode or WAF challenges can block osu! clients and bancho servers. Turn them
  off for this domain or skip them for `/api/*` and `/d/*`.

## Notes

- The first backfill is limited by the osu! API rate limit (60 req/min by default, 50
  sets per request). Expect it to take several hours. Discovery keeps running at the
  same time, so new maps show up right away.
- Memory: the search index needs very roughly 300–500 MB for the full osu! catalogue.
- Tests: `go test ./...`. The DB integration test runs when `KIRINO_TEST_DSN` is set.
