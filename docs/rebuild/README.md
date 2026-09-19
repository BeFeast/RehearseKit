# RehearseKit rebuild (`rk`) — phase 2 skeleton

The rebuild replaces FastAPI + Celery + Redis + the websocket service with
one Go binary, `rk`, that serves the API, the embedded SPA and (from phase 3)
the CPU worker. This document covers what exists after phase 2 and how to
run it on a laptop. The legacy `backend/`, `frontend/`, `websocket/` and
`stemd/` trees are untouched until phase 5.

## Layout

```
go.mod                          module github.com/BeFeast/RehearseKit (Go 1.26)
cmd/rk/main.go                  serve | migrate | create-admin | import-legacy | worker (stub)
internal/api/                   server wiring, middleware, error envelope, SPA
internal/api/respond/           JSON + {"code","message"} helpers
internal/auth/                  argon2id passwords, cookie sessions, approval, admin, Google sign-in
internal/auth/googleid/         Google ID-token verification (JWKS cache, RS256), stdlib only
internal/jobs/                  job model, queue (SKIP LOCKED), events + NOTIFY, SSE
internal/stems/                 Range-served WAV + peaks (from stemd)
internal/storage/               on-disk layout under RK_DATA_DIR
internal/db/                    pgx pool, embedded migrations, dbtest helper
internal/pipeline/peaks/        .pk writer/reader (min/max mip pyramid)
internal/config/                RK_* environment
internal/legacy/                import-legacy: FastAPI users/jobs/stems -> rk schema + layout
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
| `RK_GOOGLE_CLIENT_ID` | empty | OAuth client id; enables `POST /api/v1/auth/google` and `google_sign_in:true` in `/api/v1/config` |
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
`X-Forwarded-For`/`X-Real-IP` are honoured only when the peer is a private or
loopback address, i.e. the reverse proxy, and the rightmost hop is the
client). Errors:
`400 invalid_url`, `422 youtube_unavailable` (private/removed/geo-blocked,
message from yt-dlp), `504 youtube_timeout`, `501 youtube_unsupported` when
`yt-dlp` is not on `PATH` - `/api/v1/config` reports that as
`"youtube_preview": false` so the SPA can hide the URL input.

Errors are always `{"code":"...","message":"..."}`. Notable codes:
`pending_approval` (403 on login), `account_inactive` (403), `invalid_credentials` (401),
`claim_token_required` (403), `expired` (410 for an anonymous link past
`expires_at`), `too_large` (413), `already_finished` (409 on cancel).

## Auth

Two ways in, one session model. Both end in the `rk_session` cookie
(HttpOnly, SameSite=Lax, Secure behind https, 30 days) issued only to
`active` accounts.

**Email + password.** `POST /auth/register` creates a `pending` account;
an admin approves it (`POST /admin/users/{id}/approve`); `POST /auth/login`
then issues the session. `rk create-admin` bootstraps the first admin.

**Google.** The SPA loads Google Identity Services with the
`google_client_id` from `/api/v1/config`, gets an ID token from the GIS
callback and posts it as-is:

```bash
curl -s -c cj -H 'Content-Type: application/json' \
  -d '{"credential":"<google id_token>"}' $B/api/v1/auth/google
```

The server verifies the token itself (`internal/auth/googleid`, no
third-party dependency): RS256 signature against Google's JWKS
(`https://www.googleapis.com/oauth2/v3/certs`, cached for the
`Cache-Control: max-age` Google sends, refetched when a token names an
unknown `kid`, at most once a minute), `iss` in
`{accounts.google.com, https://accounts.google.com}`, `aud` equal to
`RK_GOOGLE_CLIENT_ID`, `exp`/`iat` with 60 s skew, and `email_verified`.

Then the account is resolved, in this order:

1. by the stored Google `sub` (`users.google_sub`, migration `0002`), so a
   Google-side email change still lands on the same rk account;
2. by email (citext, case-insensitive) — an existing password account is
   *linked*: `google_sub` and `avatar_url` are recorded, an empty `name` is
   filled in, and `provider`/`password_hash` are left alone, so password
   login keeps working;
3. otherwise a new user is created with `provider=google`, `role=user`,
   `status=pending` — the same policy as `/auth/register`; there is no
   domain allow-list or admin-email auto-promotion (the admin is created
   with `rk create-admin`, and signing in with Google on that email links
   to the existing admin row).

Responses:

| Status | Code | When |
|---|---|---|
| 200 + cookie | — | account is `active`; body is the user |
| 403 | `pending_approval` | account is `pending`; body also carries `"user": {...}` so the SPA can render `/pending-approval` without a session |
| 403 | `account_inactive` | account was deactivated by an admin |
| 403 | `email_not_verified` | Google says the address is not verified |
| 401 | `invalid_google_token` | signature, issuer, audience, expiry or shape failed (the reason is logged at debug, not returned) |
| 503 | `google_unavailable` | JWKS could not be fetched and nothing is cached |
| 501 | `google_not_configured` | `RK_GOOGLE_CLIENT_ID` is empty |
| 400 | `invalid_json` | body is not `{"credential": "..."}` |

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

## Importing legacy data

`rk import-legacy` moves users, jobs and stems out of the FastAPI/Celery
deployment (`backend/app/models`) into the rk schema and storage layout. The
legacy database is opened read-only (`default_transaction_read_only=on`);
the legacy storage root is only read. Run it with the same `RK_DATABASE_URL`
and `RK_DATA_DIR` the server uses.

```bash
rk import-legacy --legacy-db 'postgres://user:pass@host:5432/rehearsekit' \
                 --legacy-dir /path/to/legacy/root \
                 [--dry-run] [--copy | --hardlink] [--retention-days N]
```

Legacy layout expected under `--legacy-dir`:

```
stems/<job_id>/{vocals,drums,bass,other}.wav
uploads/<job_id>_source.<ext>          original upload (wav/flac/mp3/...)
<job_id>.zip                           legacy package (not imported)
```

What it does, per row:

| Legacy | rk |
|---|---|
| `users.email` / `full_name` / `avatar_url` | `users.email` (lower-cased) / `name` / `avatar_url` |
| `oauth_provider = 'google'` | `provider = 'google'` |
| anything else (`NULL`, `email`) | `provider = 'password'`, `password_hash = NULL` (bcrypt is not migrated; the account needs a reset or Google sign-in) |
| `is_admin` | `role = admin` / `user` |
| `is_active` | `status = active` / `pending` |
| `created_at`, `last_login_at` | copied |
| user without email | skipped (its jobs become anonymous) |
| existing rk account with the same email | merged: rk id, credentials, role and status are kept; empty `name`/`avatar_url` filled, `last_login_at`/`created_at` widened; legacy job owners are re-pointed to it |
| `jobs.id` | same UUID |
| `status` `COMPLETED`/`FAILED`/`CANCELLED` | `completed`/`failed`/`cancelled` |
| any in-flight status (`PENDING` … `PACKAGING`) | `failed`, error `legacy job was <STATUS> at import time` (Celery state is gone; nothing can resume it) |
| `COMPLETED` but not all four stems on disk | `failed`, error `legacy stems missing`, no stems copied |
| `quality_mode` `fast`/`high` | `fast`/`high` |
| `detected_bpm`, `error_message`, `created_at`, `completed_at` | copied; `completed_at` defaults to `created_at` for failed imports |
| — | `expires_at = created_at + retention` (`RK_JOB_RETENTION_DAYS` or `--retention-days`) |
| — | `duration_seconds`, `sample_rate`, `channels` from the first stem header |
| `stems/<id>/<name>.wav` | `jobs/<id>/stems/<name>.wav` + row in `stems` (frames, sample_rate, bit_depth, channels from the WAV header, bytes from stat) + `jobs/<id>/peaks/<name>.pk` |
| `uploads/<id>_source.<ext>` | `jobs/<id>/source.<ext>` + `jobs/<id>/peaks/source.pk` (non-WAV sources are decoded through `ffmpeg` when it is on `PATH`; otherwise the source peaks are skipped with a warning) |
| — | one `job_events` row `imported from legacy` with the final status |

Files are copied by default. `--hardlink` links instead and falls back to a
copy per file when the link fails (different mount, read-only bind mount);
the fallback count is reported. Files are placed first, then the job, its
stems and its event are inserted in one transaction; on any error the job
directory is removed so a re-run starts clean.

Re-running is safe: users already present (by id or by email) and jobs whose
id already exists are skipped. `--dry-run` produces the same report without
writing files or rows. The report is JSON on stdout:

```json
{
  "dry_run": false,
  "users": {"imported": 1, "merged": 1, "existing": 0, "skipped": 0},
  "jobs":  {"imported": 8, "imported_completed": 8, "imported_failed": 0, "existing": 0, "errors": 0},
  "stems": 32, "peaks": 38, "bytes_copied": 3852000000, "hardlink_fallbacks": 0,
  "user_results": [{"legacy_id": "...", "email": "...", "action": "merged", "id": "...", "reason": "..."}],
  "job_results":  [{"id": "...", "action": "imported", "status": "completed", "stems": 4, "source": "source.flac", "source_peaks": true, "bytes": 123}]
}
```

The exit status is non-zero when any job ended in `"action": "error"`.

In Docker, mount the legacy root read-only next to the data volume:

```bash
docker run --rm --env-file .env -v /srv/rk/data:/data -v /srv/rehearsekit-legacy:/legacy:ro \
  rk:local import-legacy --legacy-db 'postgres://...' --legacy-dir /legacy --dry-run
```

Take a `pg_dump` of the target database before the real run.

## Not in this phase

`rk worker` (exits "not implemented"), the SPA, `/jobs/{id}/reprocess`,
`/jobs/{id}/download`, `/jobs/{id}/mix`, `/profile`, the GPU-runner endpoints
and retention cleanup.
