# RehearseKit GPU runner: demucs on CUDA + the transcription adapters + the
# rk binary running `rk gpu-agent`.
#
#   docker build -f deploy/gpu-runner/Dockerfile -t ghcr.io/kossoy/rk-gpu-runner:<tag> .
#
# Build context is the repository root (the Go module is compiled in the
# first stage). Models are downloaded at build time so a fresh instance
# never waits on dl.fbaipublicfiles.com / Hugging Face.
#
# MuScriptor weights are gated (CC BY-NC 4.0, accept on huggingface.co):
# download them on the build host into deploy/gpu-runner/models/muscriptor/
# <size>/{model.safetensors,config.json} (gitignored) before building; the
# HF token never enters the image. Without them the guitar/bass/piano
# adapters fall back to hf_midi_transcription (RK_ADAPTER_* defaults).

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
    HF_HOME=/models/hf \
    HF_HUB_OFFLINE=1 \
    RK_MODELS_DIR=/models \
    RK_DEMUCS_DEVICE=cuda \
    RK_WORK_DIR=/work \
    RK_TRANSCRIBE=1 \
    RK_TRANSCRIBE_TOOLS=/opt/rk/tools/transcribe

RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates openssh-server curl git libsndfile1 \
 && rm -rf /var/lib/apt/lists/*

# demucs 4.0.1 pins nothing that conflicts with the torch already in the image.
RUN pip install --no-cache-dir "demucs==4.0.1" "soundfile>=0.12"

# Transcription stack, pinned for torch 2.2 / numpy<2 (see deploy/gpu-runner/README.md):
#  - beat_this 1.1.0 (MIT) needs rotary-embedding-torch<0.9 (0.9 wants torch>=2.4)
#  - muscriptor 0.3.0 (code MIT, weights CC BY-NC): the PyPI release still allows torch 2.2
#  - ADTOF-pytorch and hf_midi_transcription are git-only (MIT)
COPY deploy/gpu-runner/requirements-transcribe.txt /tmp/requirements-transcribe.txt
RUN pip install --no-cache-dir -r /tmp/requirements-transcribe.txt \
 && python -c "import demucs, torch, beat_this, muscriptor, adtof_pytorch, hf_midi_transcription; print('transcription stack ok, torch', torch.__version__)"

# Pre-download the three demucs models the quality presets map to (~600 MB),
# the Beat This! checkpoint (~78 MB) and the hf_midi checkpoints (~300 MB).
RUN python -c "from demucs.pretrained import get_model; [get_model(n) for n in ('htdemucs', 'htdemucs_ft', 'htdemucs_6s')]" \
 && curl -fsSL -o /models/beat_this-final0.ckpt https://cloud.cp.jku.at/public.php/dav/files/7ik4RrBKTS273gp/final0.ckpt \
 && mkdir -p /models/hf_midi \
 && HF_HUB_OFFLINE=0 python -c "from huggingface_hub import hf_hub_download; import shutil; [shutil.copy(hf_hub_download('xavriley/midi-transcription-models', f), '/models/hf_midi/'+f) for f in ('guitar-gaps.pth','filobass_20000_iterations.pth','piano.pth')]" \
 && rm -rf /models/hf/hub/models--xavriley--midi-transcription-models \
 && du -sh /models

# Optional gated MuScriptor weights from the build host (empty dir when absent).
COPY deploy/gpu-runner/models/ /models/

COPY tools/transcribe /opt/rk/tools/transcribe
COPY --from=build /out/rk /usr/local/bin/rk
RUN mkdir -p /work && rk help 2>/dev/null || true

# vast.ai launches images in "ssh" mode with its own entrypoint and runs the
# on-start command; running the image directly starts the agent.
ENTRYPOINT ["rk"]
CMD ["gpu-agent"]
