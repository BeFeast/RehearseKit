#!/usr/bin/env python3
"""Drum transcription with ADTOF-pytorch (5 classes): drums.wav → notes.json.

Class → General MIDI note used by Superior Drummer 3 on the GM map:
kick 36, snare 38, toms 48, hi-hat (closed) 42, crash/ride 49. Note 50 is
avoided on purpose (SD3 default: cymbal choke). Each hit gets a short
fixed duration; the worker overrides drum durations anyway.
"""
from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rk_adapter as rk  # noqa: E402

# ADTOF LABELS_5 = [35, 38, 47, 42, 49] → GM notes for SD3.
GM_MAP = {35: 36, 38: 38, 47: 48, 42: 42, 49: 49}
HIT_SECONDS = 0.1


def main() -> None:
    args = rk.parse_args("notes")
    device = rk.resolve_device(args.device)
    try:
        import torch
        from adtof_pytorch import (
            FRAME_RNN_THRESHOLDS,
            LABELS_5,
            PeakPicker,
            calculate_n_bins,
            create_frame_rnn_model,
            get_default_weights_path,
            load_audio_for_model,
            load_pytorch_weights,
        )
    except ImportError as e:  # pragma: no cover
        rk.fail(f"adtof_pytorch not installed: {e}")
    model = create_frame_rnn_model(calculate_n_bins()).eval()
    model = load_pytorch_weights(model, get_default_weights_path(), strict=False).to(device)
    x = load_audio_for_model(args.input).to(device)
    with torch.no_grad():
        act = model(x).cpu().numpy()  # [1, T, 5] @ 100 fps
    peaks = PeakPicker(FRAME_RNN_THRESHOLDS, fps=100).pick(act, labels=LABELS_5)[0]
    notes = []
    for label, onsets in peaks.items():
        pitch = GM_MAP.get(int(label), int(label))
        for t in onsets:
            # Velocity from the activation strength at the onset frame.
            frame = min(act.shape[1] - 1, int(round(float(t) * 100)))
            col = LABELS_5.index(int(label))
            strength = float(act[0, frame, col])
            vel = 0.5 + 0.5 * min(1.0, strength)
            notes.append({"onset": float(t), "offset": float(t) + HIT_SECONDS, "pitch": pitch, "velocity": vel})
    rk.write_json(args.output, rk.notes_result(args.stem, "adtof", "adtof_frame_rnn", notes))


if __name__ == "__main__":
    main()
