# stemd — stem file server (streaming playback lab)

Minimal Go service that serves RehearseKit stem WAVs with full HTTP Range
support. It is the backend half of the streaming playback lab
(`frontend/app/lab/stream`): the browser pulls byte ranges of each stem into an
AudioWorklet ring buffer instead of decoding whole files.

It is an architecture probe, not part of the product stack: it is not in
`docker-compose.yml` and the FastAPI backend is untouched.

## Run

```bash
cd stemd
go build -o stemd .
STEMD_ROOT=/tmp/storage STEMD_ADDR=127.0.0.1:8010 STEMD_CORS_ORIGINS=http://localhost:3000 ./stemd
```

Layout on disk mirrors the backend's `LOCAL_STORAGE_PATH`:
`<root>/stems/<job-uuid>/<stem>.wav`.

| Route | Response |
|---|---|
| `GET /healthz` | `{"status":"ok"}` |
| `GET /stems/{job}` | JSON manifest: stems present, byte sizes, URLs |
| `GET`/`HEAD /stems/{job}/{stem}` | the WAV, `200` or `206` with `Content-Range`, `Accept-Ranges: bytes`, `416` when unsatisfiable |

Every response carries `Cross-Origin-Resource-Policy: cross-origin`; allowed
origins get `Access-Control-Allow-Origin` and
`Access-Control-Expose-Headers: Content-Range, Accept-Ranges, Content-Length`.

## Test

```bash
cd stemd && go vet ./... && go test ./...
```
