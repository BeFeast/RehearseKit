#!/usr/bin/env python3
"""Beat grid with Beat This! (CPJKU, MIT): mix.wav → grid.json.

Output: {"beats": [s...], "downbeats": [s...], "source": "beat_this", "model": "final0"}.
Checkpoint: $RK_MODELS_DIR/beat_this-final0.ckpt (baked into the image), else
the torch hub cache / download.
"""
from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rk_adapter as rk  # noqa: E402

MODEL = os.environ.get("RK_BEATTHIS_MODEL", "final0")


def main() -> None:
    args = rk.parse_args("grid")
    device = rk.resolve_device(args.device)
    try:
        from beat_this.inference import Audio2Beats
    except ImportError as e:  # pragma: no cover
        rk.fail(f"beat_this not installed: {e}")
    ckpt = os.path.join(rk.MODELS_DIR, f"beat_this-{MODEL}.ckpt")
    if not os.path.exists(ckpt):
        ckpt = MODEL  # torch hub cache or download
    signal, sr = rk.load_audio(args.input, mono=True)
    a2b = Audio2Beats(checkpoint_path=ckpt, device=device, float16=False, dbn=False)
    beats, downbeats = a2b(signal, sr)
    beats = [round(float(b), 4) for b in beats]
    downbeats = [round(float(d), 4) for d in downbeats]
    if len(beats) < 8:
        rk.fail(f"beat_this found only {len(beats)} beats")
    rk.write_json(args.output, {"beats": beats, "downbeats": downbeats, "source": "beat_this", "model": MODEL})


if __name__ == "__main__":
    main()
