# RehearseKit rebuild (`rk`) — phase 2 skeleton

The rebuild replaces FastAPI + Celery + Redis + the websocket service with
one Go binary, `rk`, that serves the API, the embedded SPA and (from phase 3)
the CPU worker. This document covers what exists after phase 2 and how to
run it on a laptop. The legacy `backend/`, `frontend/`, `websocket/` and
`stemd/` trees are untouched until phase 5.

## Layout

```
go.mod                          module github.com/BeFeast/RehearseKit (Go 1.26)
cmd/rk/main.go                  serve | migrate | create-admin | worker (stub)
internal/api/                   server wiring, middleware, error envelope, SPA
internal/api/respond/           JSON + {"code","message"} helpers
internal/auth/                  argon2id passwords, cookie sessions, approval, admin
internal/jobs/                  job model, queue (SKIP LOCKED), events + NOTIFY, SSE
internal/stems/                 Range-served WAV + peaks (from stemd)
internal/storage/               on-disk layout under RK_DATA_DIR
internal/db/                    pgx pool, embedded migrations, dbtest helper
internal/pipeline/peaks/        .pk writer/reader (min/max mip pyramid)
internal/config/                RK_* environment
web/                            embedded web/dist (placeholder index.html)
```

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `RK_DATABASE_URL` | — (required) | Postgres DSN |
| `RK_LISTEN_ADDR` | `:8080` | HTTP listen address |
| `RK_DATA_DIR` | `./data` | storage root; jobs live in `jobs/<id>/` |
| `RK_CORS_ORIGINS` | empty (same-origin) | comma-separated origins allowed with credentials; `*` allowed without |
| `RK_JOB_RETENTION_DAYS` | `7` | retention for signed-in users' jobs (anonymous jobs: 24 h, fixed) |
| `RK_MAX_UPLOAD_BYTES` | `1073741824` | multipart upload cap |
| `RK_GOOGLE_CLIENT_ID` | empty | reported by `/api/v1/config` only; Google sign-in comes later |
| `RK_LOG_LEVEL` | `INFO` | slog level |

## Run locally

```bash
# 1. Postgres (any 16+; the citext extension is created by the migration)
docker run -d --name rk-pg -p 127.0.0.1:15432:5432 \
  -e POSTGRES_USER=rk -e POSTGRES_PASSWORD=rk -e POSTGRES_DB=rk postgres:16-alpine

export RK_DATABASE_URL=postgres://rk:rk@127.0.0.1:15432/rk
export RK_DATA_DIR=/tmp/rk-data

# 2. Schema (idempotent; records versions in schema_migrations)
go run ./cmd/rk migrate

# 3. An admin so you can sign in (re-running resets the password)
go run ./cmd/rk create-admin --email admin@example.com --password 'change-me-please'

# 4. Serve
go run ./cmd/rk serve
```

`GET /` answers with the placeholder page until the SPA is built into
`web/dist` in phase 4.

## curl tour

```bash
B=http://127.0.0.1:8080

curl -s $B/healthz; curl -s $B/readyz
curl -s $B/api/v1/config

# sign in (cookie rk_session; HttpOnly, SameSite=Lax, Secure behind https)
curl -s -c cj -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"change-me-please"}' $B/api/v1/auth/login
curl -s -b cj $B/api/v1/auth/me

# create a job from an upload (streams to $RK_DATA_DIR/jobs/<id>/source.mp3)
curl -s -b cj -F file=@song.mp3 -F project_name=Song -F quality=high $B/api/v1/jobs
# ... or from YouTube
curl -s -b cj -F input_url='https://www.youtube.com/watch?v=...' $B/api/v1/jobs

# anonymous callers get a one-time claim_token in the 201 body
curl -s -F file=@song.mp3 $B/api/v1/jobs
# later, signed in:
curl -s -b cj -H 'Content-Type: application/json' -d '{"claim_token":"..."}' $B/api/v1/jobs/<id>/claim

curl -s -b cj '$B/api/v1/jobs?status=active&page=1&page_size=20&q=song'
curl -s -b cj $B/api/v1/jobs/<id>
curl -s -b cj -X POST $B/api/v1/jobs/<id>/cancel
curl -s -b cj -X DELETE $B/api/v1/jobs/<id>

# progress as Server-Sent Events (replays after Last-Event-ID, ends on a terminal status)
curl -N -b cj -H 'Last-Event-ID: 0' $B/api/v1/jobs/<id>/events

# stems and peaks with Range (written by the worker in phase 3)
curl -b cj -H 'Range: bytes=0-65535' -o part.wav -D - $B/api/v1/jobs/<id>/stems/vocals
curl -b cj -o vocals.pk $B/api/v1/jobs/<id>/stems/vocals/peaks

# admin approval of self-registered accounts
curl -s -b cj '$B/api/v1/admin/users?status=pending'
curl -s -b cj -X POST $B/api/v1/admin/users/<user-id>/approve
```

## YouTube preview

`POST /api/v1/youtube/preview` with `{"url":"https://youtu.be/<id>"}` returns
`{video_id, title, channel, duration_seconds, thumbnail_url, webpage_url}` so
the SPA can show what it is about to import. Accepted shapes: `watch?v=`,
`/shorts/`, `/embed/`, `/live/`, `youtu.be/<id>` on `youtube.com`, `www.`,
`m.` and `music.youtube.com`; the id is re-emitted as a canonical watch URL
before `yt-dlp --dump-single-json --no-playlist --skip-download` runs (20 s
timeout, at most 4 processes at once). No session is needed. Successful
lookups are cached in memory (100 entries, 10 min, keyed by video id) and
each client IP gets 10 requests/min (`429 rate_limited` with `Retry-After`;
behind a proxy the rightmost `X-Forwarded-For` hop is the client). Errors:
`400 invalid_url`, `422 youtube_unavailable` (private/removed/geo-blocked,
message from yt-dlp), `504 youtube_timeout`, `501 youtube_unsupported` when
`yt-dlp` is not on `PATH` - `/api/v1/config` reports that as
`"youtube_preview": false` so the SPA can hide the URL input.

Errors are always `{"code":"...","message":"..."}`. Notable codes:
`pending_approval` (403 on login), `invalid_credentials` (401),
`claim_token_required` (403), `expired` (410 for an anonymous link past
`expires_at`), `too_large` (413), `already_finished` (409 on cancel).

## Tests

```bash
gofmt -l . && go vet ./...
RK_TEST_DATABASE_URL=postgres://rk:rk@127.0.0.1:15432/rk go test ./...
```

Database-backed tests skip when `RK_TEST_DATABASE_URL` is unset. Each test
gets its own schema (`rk_test_<hex>`) with `search_path` pointed at it, so
packages run in parallel against one database and clean up after themselves.

## Peaks file (`.pk`)

Little-endian: `"RKPK"`, `u16 version=1`, `u16 channels`, `u32 sampleRate`,
`u64 frames`, `u8 stages`, then per stage `u8 shift`, `u32 numPeaks`,
`u64 dataOffset`. Stage data is channel-major: for each channel, `numPeaks`
pairs of `int8 min, int8 max` (scale: `round(clamp(x,-1,1)*127)`). Default
stages: shift 6, 9, 12, 15. See `internal/pipeline/peaks`.

## Not in this phase

Google OIDC (`POST /api/v1/auth/google` answers 501), `rk worker`
(exits "not implemented"), the SPA, `/jobs/{id}/reprocess`,
`/jobs/{id}/download`, `/jobs/{id}/mix`, `/profile`, the GPU-runner endpoints,
retention cleanup and `import-legacy`.
