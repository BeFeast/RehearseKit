# RehearseKit GPU runner: demucs on CUDA + the rk binary running `rk gpu-agent`.
#
#   docker build -f deploy/gpu-runner/Dockerfile -t ghcr.io/kossoy/rk-gpu-runner:<tag> .
#
# Build context is the repository root (the Go module is compiled in the
# first stage). Models are downloaded at build time so a fresh instance
# never waits on dl.fbaipublicfiles.com.

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/rk ./cmd/rk

# torch 2.2 / CUDA 12.1 runtime (python 3.10). Needs an NVIDIA driver >= 525.
FROM pytorch/pytorch:2.2.0-cuda12.1-cudnn8-runtime

ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONUNBUFFERED=1 \
    TORCH_HOME=/models \
    RK_DEMUCS_DEVICE=cuda \
    RK_WORK_DIR=/work

RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates openssh-server curl \
 && rm -rf /var/lib/apt/lists/*

# demucs 4.0.1 pins nothing that conflicts with the torch already in the image.
RUN pip install --no-cache-dir "demucs==4.0.1" "soundfile>=0.12"

# Pre-download the three models the quality presets map to (~600 MB).
RUN python -c "from demucs.pretrained import get_model; [get_model(n) for n in ('htdemucs', 'htdemucs_ft', 'htdemucs_6s')]" \
 && du -sh /models

COPY --from=build /out/rk /usr/local/bin/rk
RUN mkdir -p /work && rk help 2>/dev/null || true

# vast.ai launches images in "ssh" mode with its own entrypoint and runs the
# on-start command; running the image directly starts the agent.
ENTRYPOINT ["rk"]
CMD ["gpu-agent"]
