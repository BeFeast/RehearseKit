# RehearseKit rebuild (`rk`) — phases 2–4

The rebuild replaces FastAPI + Celery + Redis + the websocket service with
one Go binary, `rk`, that serves the API, the embedded SPA, the CPU worker
and the GPU runner. This document covers what exists after phase 3 and how
to run it on a laptop. The legacy `backend/`, `frontend/`, `websocket/` and
`stemd/` trees are untouched until phase 5.

## Layout

```
go.mod                          module github.com/BeFeast/RehearseKit (Go 1.26)
cmd/rk/main.go                  serve | migrate | create-admin | import-legacy | worker | gpu-agent
internal/api/                   server wiring, middleware, error envelope, SPA
internal/api/respond/           JSON + {"code","message"} helpers
internal/auth/                  argon2id passwords, cookie sessions, approval, admin, Google sign-in
internal/auth/googleid/         Google ID-token verification (JWKS cache, RS256), stdlib only
internal/jobs/                  job model, queue (SKIP LOCKED), events + NOTIFY, SSE,
                                stage bands + legacy status copy (stages.go)
internal/stems/                 Range-served WAV + peaks (from stemd)
internal/storage/               on-disk layout under RK_DATA_DIR
internal/db/                    pgx pool, embedded migrations, dbtest helper
internal/signed/                HMAC signed URLs; GET source / PUT stem endpoints
internal/gpu/                   GPU lease API (claim, heartbeat, complete, fail, expiry)
internal/worker/                rk worker: CPU stages, GPU wait, sweepers
internal/agent/                 rk gpu-agent: lease → demucs → upload loop
internal/pipeline/peaks/        .pk writer/reader (min/max mip pyramid)
internal/pipeline/media/        ffprobe / ffmpeg / yt-dlp wrappers (ctx-killed)
internal/pipeline/tempo/        tempo.json model + runner for tools/tempo/tempo.py
internal/pipeline/demucs/       demucs runner + tqdm progress folding
internal/pipeline/dawproject/   DAWproject 1.0 writer (golden-tested XML)
internal/pipeline/pack/         package.zip + README
internal/pipeline/wavcheck/     24-bit/48 kHz/stereo WAV validation
internal/config/                RK_* environment
internal/legacy/                import-legacy: FastAPI users/jobs/stems -> rk schema + layout
tools/tempo/tempo.py            librosa tempo analyser (confidence-gated)
deploy/gpu-runner/              CUDA image for the runner + vast.ai runbook
web/                            the SPA (Vite + React); web/dist is embedded, see "SPA (web/)"
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
| `RK_RUNNER_TOKEN` | empty | bearer token for GPU runners on `/api/v1/gpu/*`; empty disables that API (503 `gpu_disabled`) |
| `RK_SIGNING_KEY` | derived from `RK_RUNNER_TOKEN` | HMAC key for signed source/stem URLs |
| `RK_PUBLIC_URL` | derived from the request | base of the absolute signed URLs handed to runners |
| `RK_GPU_LEASE_TTL` | `10m` | a lease without a heartbeat for this long expires and the job is re-offered |
| `RK_SIGNED_URL_TTL` | `2h` | lifetime of the signed URLs in a lease |
| `RK_MAX_DURATION_SECONDS` | `1800` | worker rejects longer sources (`Audio is too long: …`) |
| `RK_PYTHON` | `python3` | interpreter for `tools/tempo/tempo.py` (needs librosa) and local demucs |
| `RK_TOOLS_DIR` | `./tools` | where `tempo/tempo.py` lives |
| `RK_TEMPO_CMD` | empty | replaces the analyser command entirely (tests) |
| `RK_LOCAL_DEMUCS` | unset | `1` makes the worker run `python -m demucs` itself (dev; no GPU runner needed) |
| `RK_DEMUCS_DEVICE` | `cpu` (worker) / `cuda` (agent) | demucs `-d` |
| `RK_GPU_WAIT_TIMEOUT` | `3h` | a job waiting in `separating` with no runner ever leasing it fails after this |

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

# 4. Serve (RK_RUNNER_TOKEN enables the GPU lease API)
export RK_RUNNER_TOKEN=$(openssl rand -hex 24)
go run ./cmd/rk serve

# 5. Worker (needs ffmpeg, ffprobe, yt-dlp on PATH and a python with librosa)
python3 -m venv .venv && .venv/bin/pip install -r tools/tempo/requirements.txt
RK_PYTHON=$PWD/.venv/bin/python go run ./cmd/rk worker

# 6a. Separation on a GPU box (see deploy/gpu-runner/README.md), or
# 6b. locally for development (slow on CPU; needs `pip install demucs` in RK_PYTHON)
RK_LOCAL_DEMUCS=1 RK_PYTHON=$PWD/.venv/bin/python go run ./cmd/rk worker
```

`GET /` answers with the placeholder page until the SPA is built into
`web/dist` (`cd web && bun run build`, then rebuild `rk`; see "SPA (web/)").

## Pipeline (`rk worker`)

One process, one job at a time. `jobs.Claim` takes the oldest `pending`
job; each stage owns a band of the 0–100 progress and emits `job_events`
with the legacy caption strings (`jobs.StatusMessage`, verbatim from the
old `job-card.tsx`) so the SPA can keep its copy.

| Stage | Progress | Work | Output under `jobs/<id>/` |
|---|---|---|---|
| converting | 0–14 | ffprobe duration check (≤ `RK_MAX_DURATION_SECONDS`), `yt-dlp -x --audio-format wav` for YouTube jobs, `ffmpeg -ar 48000 -ac 2 -c:a pcm_s24le` | `source.wav` |
| analyzing | 14–28 | `tools/tempo/tempo.py` (librosa), source peaks, `jobs.detected_bpm/duration_seconds/sample_rate/channels` | `tempo.json`, `peaks/source.pk` |
| separating | 28–76 | GPU lease (below) or `RK_LOCAL_DEMUCS=1`; progress from runner heartbeats | `stems/<name>.wav` |
| finalizing | 76–89 | verify each stem is 24-bit/48 kHz/stereo, per-stem peaks, `stems` rows, DAWproject | `peaks/<name>.pk`, `project.dawproject` |
| packaging | 89–99 | zip (stems stored, text deflated) | `package.zip` |
| completed | 100 | | |

Every external tool runs under a context: a stage timeout (20 min
convert, 15 min analyse, 20 min finalise/package) or a job cancel
(`POST /jobs/{id}/cancel`, polled every 2 s) kills the child process
and the run stops; a cancelled job is left `cancelled`, anything else
becomes `failed` with a user-facing message (`Audio is too long: 35:12
exceeds the 30:00 limit`, `Conversion failed: …`, `Stem separation
failed after 3 attempts: …`).

Tempo: `bpm` is `null` when the analyser's confidence (mean of local-tempo
consistency and inter-beat regularity, see the docstring in `tempo.py`) is
below 0.5. A null tempo yields a 120 BPM placeholder in the DAWproject with
a Comment in `metadata.xml` and the README; the audio still plays at the
original speed because the clip warps map seconds onto beats.

DAWproject (`project.dawproject`): zip with `project.xml`
(Application, Transport/Tempo + TimeSignature, Structure with one
`Track/Channel` per stem routed to a master, Arrangement/Lanes
(timeUnit=beats) → Lanes(track) → Clips → Clip → Warps
(contentTimeUnit=seconds) → Audio/File + two Warp markers `(0,0)` and
`(songBeats, songSeconds)`), `metadata.xml` (Title, Year, Comment) and
`audio/<name>.wav` copies. Golden test:
`internal/pipeline/dawproject/testdata/project.xml` (`go test -update`
to regenerate).

Sweepers (in the worker process): expired GPU leases every 30 s, jobs stuck
in a CPU stage with no event for 45 min every 5 min (→ failed), retention
(`DELETE … WHERE expires_at < now()` + directory removal) every 10 min.
On start the worker adopts jobs left in `separating`/`finalizing`/
`packaging` by a previous process (`--adopt=false` to disable when running
several workers); on SIGTERM a job still in converting/analyzing goes back
to `pending`.

## GPU runners (`rk gpu-agent`)

Pull model — the GPU box needs no inbound port and no database access.

```
runner                                   rk serve
  POST /api/v1/gpu/lease  ──────────────▶ oldest job in `separating` with no active lease
  ◀── {lease_id, job_id, model, stems, source_url, upload_urls{stem→PUT}, expires_at, heartbeat_seconds}
  GET  source_url (signed, 2 h) ────────▶ jobs/<id>/source.wav
  python -m demucs -n <model> --flac --int24 -d cuda
  POST /gpu/lease/{id}/heartbeat {progress 0..1} ─▶ extends TTL, job progress 28–76 %
  ffmpeg flac → 24-bit/48 kHz wav
  PUT  upload_urls[stem] (Content-Length ≤ 2 GiB) ─▶ jobs/<id>/stems/<stem>.wav (temp + rename)
  POST /gpu/lease/{id}/complete {stems:[{name,bytes,sha256}]} ─▶ verify size/sha/WAV header → job finalizing
  POST /gpu/lease/{id}/fail {error} ────▶ lease failed; job re-offered, or failed after 3 attempts
```

All `/api/v1/gpu/*` calls carry `Authorization: Bearer $RK_RUNNER_TOKEN`
(401 `runner_unauthorized`). Lease errors: 404 `lease_not_found`, 403
`lease_owner` (another runner id), 410 `lease_closed` (expired/finished),
409 `job_gone` (cancelled — the agent kills demucs), 400 `bad_stems`.
An empty queue answers 204.

Signed URLs are `path?exp=<unix>&sig=<base64url HMAC-SHA256(method\npath\nexp)>`
over `RK_SIGNING_KEY`; the method is part of the MAC so a GET link can not
be replayed as a PUT. Served by `GET/HEAD /api/v1/signed/jobs/{id}/source`
and `PUT /api/v1/signed/jobs/{id}/stems/{name}`.

Leases live in `gpu_leases`; at most one is active per job (partial unique
index, migration 0002). A lease that misses heartbeats for
`RK_GPU_LEASE_TTL` is expired by the worker sweeper and the job goes back
to waiting; three failed/expired leases fail the job.

Image, flags and the vast.ai rent → run → destroy flow:
[`deploy/gpu-runner/README.md`](../../deploy/gpu-runner/README.md).

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

# stems and peaks with Range (written by the worker)
curl -b cj -H 'Range: bytes=0-65535' -o part.wav -D - $B/api/v1/jobs/<id>/stems/vocals
curl -b cj -o vocals.pk $B/api/v1/jobs/<id>/stems/vocals/peaks

# the package (409 not_ready until the job is completed; Range supported)
curl -b cj -OJ $B/api/v1/jobs/<id>/download

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
The worker integration test (`internal/worker`) needs ffmpeg/ffprobe on
PATH (skips otherwise), uses a stub tempo command and a fake in-process GPU
runner, and exercises upload → convert → analyse → lease → finalise →
package on a synthetic WAV.

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

`/jobs/{id}/reprocess`, `/jobs/{id}/mix`, `/profile` and the SPA (phase 4).
## SPA (`web/`)
Phase 4: the new front end, a Vite 6 + React 19 + TypeScript SPA that the
`rk` binary embeds. It renders every screen of the design handoff (landing /
upload, job history, job detail with the stem mixer, sign-in dialog, pending
approval, profile, user management, 404 / error boundary), talks only to
`/api/v1`, and plays stems through the streaming engine ported from the
`frontend/lib/lab-stream` prototype (SharedArrayBuffer ring buffers + an
AudioWorklet, HTTP Range chunks, one shared read cursor so stems never drift).
### Layout
web/package.json                bun scripts: dev | build | preview | lint | typecheck | test | shots
web/vite.config.ts              React + Tailwind 4 plugins, COOP/COEP headers, /api proxy, vitest
web/index.html                  applies the persisted theme before first paint
web/public/stream-processor.js  the AudioWorklet (plain JS, served at /stream-processor.js)
web/src/styles/                 tokens.css + screen.css (handoff, verbatim) + app.css (@theme + additions)
web/src/api/                    typed client (snake_case wire types), SSE subscriber
web/src/auth/                   session provider, sign-in / register dialog
web/src/lib/                    decibel law, timecode/bars, .pk parser, stage copy, mix-state reducer
web/src/player/engine/          streaming engine (+ solo / meters / loop A-B), vitest suite
web/src/player/use-mixer.ts     engine + mix state + peaks wired into one handle (window.__rk for scripts)
web/src/components/             header, dialogs, toasts, job row, upload form, mixer strips, mobile player
web/src/routes/                 one file per route (TanStack Router, code-based tree in src/router.tsx)
web/scripts/shot.mjs            full-page screenshots of every route through a CDP Chrome
web/scripts/playback-verify.mjs drives the mixer over CDP and reports underruns / meters / seeks
web/embed.go                    //go:embed dist, placeholder fallback when the SPA is not built
web/dist/                       Vite output; only .gitkeep and placeholder.html are tracked
### Develop
cd web
bun install
bun run dev          # http://127.0.0.1:5173, proxies /api, /healthz, /readyz to rk on :8080
bun run typecheck    # tsc -b
bun run lint         # eslint
bun run test         # vitest (engine, decibel, format, peaks, mix-state, stages, api, RTL)
The dev server sends `Cross-Origin-Opener-Policy: same-origin` and
`Cross-Origin-Embedder-Policy: credentialless` on every response, which the
player needs for `SharedArrayBuffer`. `rk serve` sends the same pair on
`/jobs` and `/jobs/*` (see `internal/api/static.go`).
### Build and embed
cd web && bun run build      # clears dist/assets, tsc -b, vite build → web/dist
cd .. && go build ./cmd/rk   # embeds web/dist
`web/dist` is gitignored except for `.gitkeep` and `placeholder.html`.
`web.Dist()` serves `placeholder.html` as `index.html` when no Vite build is
present, so `go build ./...` and `rk serve` work from a clean checkout; a
built `index.html` takes precedence. Do not commit `web/dist/index.html` or
`web/dist/assets/`.
JetBrains Mono is self-hosted through `@fontsource/jetbrains-mono`
(woff2 in the bundle); the handoff's Google Fonts `@import` was removed.
### What degrades until phase 3
The SPA is written against the full design; routes the API does not have
yet fall back rather than break:
| Route | Behaviour now |
| `POST /auth/google` (501) | Google button shows "coming back soon"; email + password works |
| `POST /youtube/preview` (404) | URL preview card reads "Preview unavailable — continue anyway" |
| `GET/PUT /jobs/{id}/mix` (404) | mix state (faders, mute/solo, loop, selected strip) mirrors to `localStorage` |
| `GET /jobs/{id}/download` (404) | Download button probes with HEAD and explains the package lands with the worker |
| `GET/PATCH /profile` (404) | profile reads `/auth/me`; saving shows the error banner |
| reprocess / retry | buttons explain that requeueing lands with the worker |
| pending approval | the page polls `/auth/me` every 30 s; a pending password account has no session, so it flips to "You're in" only once a session exists |
### A completed job by hand (`rk peaks`)
Until the worker exists, a completed job can be assembled for the mixer:
symlink stem WAVs into `$RK_DATA_DIR/jobs/<id>/stems/{vocals,drums,bass,other}.wav`,
write the peaks with `rk peaks <id>` (writes `jobs/<id>/peaks/*.pk` using
`internal/pipeline/peaks`), then insert the `jobs` row (`status =
'completed'`, `detected_bpm`, `duration_seconds`) and one `stems` row per
file (`frames`, `sample_rate`, `bit_depth`, `channels`, `peaks_path`).
### Screenshots and playback verification
Both scripts talk to a headless Chrome over CDP (the `rk-chrome` container on
`http://127.0.0.1:19222`, started with `--autoplay-policy=no-user-gesture-required`).
# every route × {1280, 390} × {light, dark}, signed out and signed in
node web/scripts/shot.mjs http://127.0.0.1:19222 http://127.0.0.1:8080 /tmp/spa-shots \
  --login admin@example.com:change-me-please --job <completed-id> --anon-job <anonymous-id> \
  --processing-job <id> --failed-job <id>
# 60 s of playback with seek / loop / solo / home, reporting underruns and meters
node web/scripts/playback-verify.mjs http://127.0.0.1:19222 http://127.0.0.1:8080/jobs/<id> \
  --duration 60 --seek 120@10 --loop 130,138@20 --solo 2@32 --home@45 --loop-off@50 --json run.json
Google OIDC (`POST /api/v1/auth/google` answers 501), `rk worker`
(exits "not implemented"), `/youtube/preview`, `/jobs/{id}/reprocess`,
`/jobs/{id}/download`, `/jobs/{id}/mix`, `/profile`, the GPU-runner endpoints,
retention cleanup and `import-legacy`.
