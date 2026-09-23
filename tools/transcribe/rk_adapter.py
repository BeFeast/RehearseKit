"""Shared helpers for the RehearseKit transcription adapters.

Every adapter is a CLI with the same contract (see internal/agent/transcribe.go):

    grid_<name>.py  --input mix.wav  --output grid.json  --device cuda|cpu
    notes_<name>.py --input stem.wav --output notes.json --stem <stem> --device cuda|cpu

Non-zero exit = failure; the last stderr line is the reason recorded in
analysis.json. Only the output file matters; stdout is discarded.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import tempfile

import numpy as np

MODELS_DIR = os.environ.get("RK_MODELS_DIR", "/models")


def parse_args(kind: str) -> argparse.Namespace:
    p = argparse.ArgumentParser()
    p.add_argument("--input", required=True)
    p.add_argument("--output", required=True)
    p.add_argument("--device", default="cpu")
    if kind == "notes":
        p.add_argument("--stem", required=True, choices=["drums", "bass", "guitar", "piano"])
    if kind == "sections":
        p.add_argument("--stems", default="")
    return p.parse_args()


def resolve_device(name: str) -> str:
    import torch

    if name.startswith("cuda") and not torch.cuda.is_available():
        print("cuda requested but not available, using cpu", file=sys.stderr)
        return "cpu"
    return name


def load_audio(path: str, sr: int | None = None, mono: bool = True) -> tuple[np.ndarray, int]:
    """Read a WAV as float32 (T,) or (T, C); resample with soxr when sr is set."""
    import soundfile as sf

    data, rate = sf.read(path, dtype="float32", always_2d=True)
    if mono:
        data = data.mean(axis=1)
    if sr is not None and rate != sr:
        import soxr

        data = soxr.resample(data, rate, sr)
        rate = sr
    return np.ascontiguousarray(data), rate


def write_json(path: str, obj) -> None:
    d = os.path.dirname(path) or "."
    os.makedirs(d, exist_ok=True)
    fd, tmp = tempfile.mkstemp(prefix=".", suffix=".json", dir=d)
    with os.fdopen(fd, "w") as f:
        json.dump(obj, f)
    os.replace(tmp, path)


def notes_result(stem: str, adapter: str, model: str, notes: list[dict]) -> dict:
    """Normalise note events: sorted by onset, offset > onset, velocity 0..1."""
    out = []
    for n in notes:
        onset = float(n["onset"])
        offset = float(n.get("offset", onset))
        if offset <= onset:
            offset = onset + 0.05
        vel = float(n.get("velocity", 0.8))
        out.append({
            "onset": round(onset, 4),
            "offset": round(offset, 4),
            "pitch": int(n["pitch"]),
            "velocity": round(min(1.0, max(0.0, vel)), 3),
        })
    out.sort(key=lambda n: (n["onset"], n["pitch"]))
    return {"stem": stem, "adapter": adapter, "model": model, "notes": out}


def fail(msg: str, code: int = 2) -> None:
    print(msg, file=sys.stderr)
    sys.exit(code)
