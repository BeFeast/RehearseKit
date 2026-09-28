#!/usr/bin/env python3
"""Beat grid with Beat This! (CPJKU, MIT): mix.wav → grid.json.

Output: {"beats": [s...], "downbeats": [s...], "source": "beat_this",
"model": "final0", "refine": "parabolic"}.
Checkpoint: $RK_MODELS_DIR/beat_this-final0.ckpt (baked into the image), else
the torch hub cache / download.

Peaks are picked by Beat This!'s minimal postprocessor on the 50 fps beat
logits; each peak is then refined below the 20 ms frame by fitting a
parabola through the logits of its frame and the two neighbours, and every
downbeat takes the refined time of the beat it was snapped to. Frame-exact
times make the tempo between adjacent beats jump by several per cent.
"""
from __future__ import annotations

import os
import sys

import numpy as np

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rk_adapter as rk  # noqa: E402

MODEL = os.environ.get("RK_BEATTHIS_MODEL", "final0")
FPS = 50


def refine(times: np.ndarray, logits: np.ndarray) -> np.ndarray:
    """Sub-frame peak times: parabolic interpolation of the logits around
    the strongest frame of each peak (a deduplicated peak can sit between
    two frames), offset clamped to half a frame."""
    out = np.empty(len(times))
    n = len(logits)
    for i, t in enumerate(times):
        lo, hi = int(np.floor(t * FPS)), int(np.ceil(t * FPS))
        f = lo if logits[min(lo, n - 1)] >= logits[min(hi, n - 1)] else hi
        f = min(max(f, 0), n - 1)
        d = 0.0
        if 0 < f < n - 1:
            a, b, c = logits[f - 1], logits[f], logits[f + 1]
            den = a - 2 * b + c
            if den < 0:
                d = float(np.clip(0.5 * (a - c) / den, -0.5, 0.5))
        out[i] = (f + d) / FPS
    return out


def main() -> None:
    args = rk.parse_args("grid")
    device = rk.resolve_device(args.device)
    try:
        from beat_this.inference import Audio2Frames
        from beat_this.model.postprocessor import Postprocessor
    except ImportError as e:  # pragma: no cover
        rk.fail(f"beat_this not installed: {e}")
    ckpt = os.path.join(rk.MODELS_DIR, f"beat_this-{MODEL}.ckpt")
    if not os.path.exists(ckpt):
        ckpt = MODEL  # torch hub cache or download
    signal, sr = rk.load_audio(args.input, mono=True)
    a2f = Audio2Frames(checkpoint_path=ckpt, device=device, float16=False)
    beat_logits, downbeat_logits = a2f(signal, sr)
    beats, downbeats = Postprocessor(type="minimal", fps=FPS)(beat_logits, downbeat_logits)
    beats, downbeats = np.asarray(beats, dtype=float), np.asarray(downbeats, dtype=float)
    if len(beats) < 8:
        rk.fail(f"beat_this found only {len(beats)} beats")
    fine = refine(beats, beat_logits.cpu().numpy())
    # Downbeats were snapped to beats by the postprocessor: take the refined
    # time of the same beat.
    down = np.unique([fine[int(np.argmin(np.abs(beats - d)))] for d in downbeats])
    rk.write_json(args.output, {
        "beats": [round(float(b), 4) for b in fine],
        "downbeats": [round(float(d), 4) for d in down],
        "source": "beat_this", "model": MODEL, "refine": "parabolic",
    })


if __name__ == "__main__":
    main()
