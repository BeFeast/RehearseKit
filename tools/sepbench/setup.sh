#!/usr/bin/env bash
# setup.sh — prepare a Vast instance (pytorch/pytorch:2.7.0-cuda12.6-cudnn9-runtime) for run.py.
# Code checkouts are pinned by commit, every checkpoint by sha256; nothing is fetched at inference time.
set -euo pipefail
cd /workspace
MSST_SHA=84b1eac0887756b4f1a9d7a1ff49105939749ed2
BSRNN_SHA=94adc8c184ac9b1d2e496fcb6e78cfe3fb78fc9c

apt-get update -qq && apt-get install -y -qq git ffmpeg iptables >/dev/null

fetch_repo() { # dir url sha
  [ -d "$1/.git" ] || git clone -q "$2" "$1"
  git -C "$1" fetch -q origin "$3" 2>/dev/null || true
  git -C "$1" checkout -q "$3"
}
fetch_repo msst https://github.com/ZFTurbo/Music-Source-Separation-Training.git "$MSST_SHA"
fetch_repo bsrnn https://github.com/magronp/bsrnn.git "$BSRNN_SHA"

# Inference subset only: MSST's full requirements pull demucs/diffq (CC BY-NC), pedalboard (GPL), wxpython.
pip install -q "numpy<2.3" scipy soundfile librosa ml_collections tqdm omegaconf==2.3.0 hydra-core==1.3.2 \
  lightning==2.5.5 beartype==0.14.1 rotary_embedding_torch==0.3.5 einops==0.8.1 demucs==4.0.1 \
  nvidia-ml-py pandas musdb==0.4.3 museval==0.4.1  # the last three: bsrnn imports them at module level

mkdir -p models && cd models
get() { # file sha256 url
  if ! echo "$2  $1" | sha256sum -c --quiet >/dev/null 2>&1; then
    curl -sSfL --retry 3 -o "$1" "$3"
    echo "$2  $1" | sha256sum -c --quiet
  fi
}
while read -r f sha url; do get "$f" "$sha" "$url"; done < /workspace/sepbench/checkpoints.txt
[ -f simo-bsrnn-opt/separator.ckpt ] || python -m zipfile -e simo-bsrnn-opt.zip .
python -c "from demucs.pretrained import get_model; get_model('htdemucs_ft')"  # baseline only, research weights
echo "egress probe: $(iptables -S OUTPUT 2>&1 | head -1)"
echo setup-ok
