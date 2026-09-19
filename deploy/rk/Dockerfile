# rk server + CPU worker image: Go binary with the embedded SPA, plus the
# CLI tools the worker shells out to (ffmpeg/ffprobe, yt-dlp, librosa tempo).
# Build from the repo root: docker build -f deploy/rk/Dockerfile -t rk:local .

FROM oven/bun:1.3-slim AS web
WORKDIR /web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /rk ./cmd/rk

FROM python:3.12-slim-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*
COPY tools/tempo/requirements.txt /app/tools/tempo/requirements.txt
RUN pip install --no-cache-dir -r /app/tools/tempo/requirements.txt yt-dlp
COPY tools/ /app/tools/
COPY --from=build /rk /usr/local/bin/rk
ENV RK_TOOLS_DIR=/app/tools RK_PYTHON=python3
WORKDIR /app
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["rk"]
CMD ["serve"]
