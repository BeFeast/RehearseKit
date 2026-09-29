# RehearseKit GPU runner images, one per model stack (internal/models):
#
#   docker build -f deploy/gpu-runner/Dockerfile --target public   -t ghcr.io/kossoy/rk-gpu-runner:public-<tag> .
#   docker build -f deploy/gpu-runner/Dockerfile --target internal -t ghcr.io/kossoy/rk-gpu-runner:internal-<tag> .
#
# public: SCNet XL IHF and MelBand RoFormer (Kim) through MSST, public-class weights only.
#   No Demucs, no BS-RoFormer SW, no transcription adapters or their weights: the image is
#   what lands on third-party Vast hosts for every public job.
# internal: public + Demucs presets + Kim → BS-RoFormer SW 6-stem HiFi + the transcription
#   adapters (Beat This!, ADTOF, hf_midi, optional MuScriptor). Owner and allowlist only.
#
# Build context is the repository root (the Go module is compiled in the first stage). Every
# checkpoint comes from internal/models/manifest.json: pinned URL, size and sha256 checked by
# fetch_models.py, which also fails the build if anything else sits under /models.
#
# MuScriptor weights are gated (CC BY-NC 4.0, accept on huggingface.co): download them on the
# build host into deploy/gpu-runner/models/muscriptor/<size>/{model.safetensors,config.json}
# (gitignored) before building the internal target; the HF token never enters the image.

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/rk ./cmd/rk

# torch 2.2 / CUDA 12.1 runtime (python 3.10). Needs an NVIDIA driver >= 525.
FROM pytorch/pytorch:2.2.0-cuda12.1-cudnn8-runtime AS base

ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONUNBUFFERED=1 \
    TORCH_HOME=/models \
    HF_HOME=/models/hf \
    HF_HUB_OFFLINE=1 \
    RK_MODELS_DIR=/models \
    RK_MODELS_MANIFEST=/opt/rk/models.json \
    RK_SEPARATE_TOOL=/opt/rk/tools/separate/separate.py \
    RK_EGRESS_DIR=/opt/rk/tools/egress \
    RK_MSST_DIR=/opt/msst \
    RK_DEMUCS_DEVICE=cuda \
    RK_WORK_DIR=/work

RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates openssh-server curl git libsndfile1 \
 && rm -rf /var/lib/apt/lists/*

COPY deploy/gpu-runner/requirements-separate.txt /tmp/requirements-separate.txt
RUN pip install --no-cache-dir -r /tmp/requirements-separate.txt

# MSST (MIT) at a pinned commit, inference code only.
ARG MSST_SHA=84b1eac0887756b4f1a9d7a1ff49105939749ed2
RUN git clone -q https://github.com/ZFTurbo/Music-Source-Separation-Training.git /opt/msst \
 && git -C /opt/msst checkout -q "$MSST_SHA" \
 && rm -rf /opt/msst/.git /opt/msst/gui /opt/msst/docs /opt/msst/tests /opt/msst/scripts \
 && cd /opt/msst && python -c "from utils.settings import get_model_from_config; from utils.model_utils import demix, apply_tta; import models.scnet, models.bs_roformer; print('msst ok')"

COPY internal/models/manifest.json /opt/rk/models.json
COPY deploy/gpu-runner/fetch_models.py /opt/rk/fetch_models.py
COPY tools/separate /opt/rk/tools/separate
COPY tools/egress /opt/rk/tools/egress
COPY --from=build /out/rk /usr/local/bin/rk
RUN mkdir -p /work /models

# vast.ai launches images in "ssh" mode with its own entrypoint and runs the
# on-start command; running the image directly starts the agent.
ENTRYPOINT ["rk"]
CMD ["gpu-agent"]

FROM base AS public
ENV RK_RUNNER_STACK=public
RUN python /opt/rk/fetch_models.py --manifest /opt/rk/models.json --dest /models --stack public \
 && python /opt/rk/fetch_models.py --manifest /opt/rk/models.json --dest /models --stack public --verify \
 && du -sh /models

FROM public AS internal
ENV RK_RUNNER_STACK=internal \
    RK_TRANSCRIBE=1 \
    RK_TRANSCRIBE_TOOLS=/opt/rk/tools/transcribe

# demucs 4.0.1 pins nothing that conflicts with the torch already in the image.
RUN pip install --no-cache-dir "demucs==4.0.1"

# Transcription stack, pinned for torch 2.2 / numpy<2 (see deploy/gpu-runner/README.md).
COPY deploy/gpu-runner/requirements-transcribe.txt /tmp/requirements-transcribe.txt
RUN pip install --no-cache-dir -r /tmp/requirements-transcribe.txt \
 && python -c "import demucs, torch, beat_this, muscriptor, adtof_pytorch, hf_midi_transcription; print('transcription stack ok, torch', torch.__version__)"

# hf_midi_transcription pulls piano_transcription_inference from git without a ref; the build
# fails if that dependency resolves to anything but the commit reviewed here.
RUN python -c "import importlib.metadata as m, json; d = json.loads(m.distribution('piano-transcription-inference').read_text('direct_url.json')); c = d['vcs_info']['commit_id']; assert c == '7568dc7f78b625e40cf9776e2806d164006610e3', c; print('piano_transcription_inference pinned', c[:7])"

RUN python /opt/rk/fetch_models.py --manifest /opt/rk/models.json --dest /models --stack internal

# Optional gated MuScriptor weights from the build host (empty dir when absent).
COPY deploy/gpu-runner/models/ /models/
COPY tools/transcribe /opt/rk/tools/transcribe
RUN python /opt/rk/fetch_models.py --manifest /opt/rk/models.json --dest /models --stack internal --verify --allow muscriptor/ \
 && du -sh /models
