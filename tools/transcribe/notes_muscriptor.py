#!/usr/bin/env python3
"""Guitar / bass / piano transcription with MuScriptor 0.3.0 (weights CC
BY-NC 4.0, gated on Hugging Face): stem.wav → notes.json.

Weights are expected pre-baked at $RK_MODELS_DIR/muscriptor/<size>/
(model.safetensors + config.json); $RK_MUSCRIPTOR_SIZE selects the size
(default medium), $RK_MUSCRIPTOR_DTYPE the dtype (default: none → fp16
autocast on CUDA, fp32 on CPU). Instruments are passed as MuScriptor's
canonical group names; MuScriptor emits no velocities, so a fixed 0.8 is
written.
"""
from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rk_adapter as rk  # noqa: E402

INSTRUMENTS = {
    "guitar": ["clean_electric_guitar", "distorted_electric_guitar", "acoustic_guitar"],
    "bass": ["electric_bass", "acoustic_bass"],
    "piano": ["acoustic_piano", "electric_piano"],
}
SIZE = os.environ.get("RK_MUSCRIPTOR_SIZE", "medium")
DTYPE = os.environ.get("RK_MUSCRIPTOR_DTYPE") or None


def main() -> None:
    args = rk.parse_args("notes")
    if args.stem not in INSTRUMENTS:
        rk.fail(f"muscriptor adapter does not handle {args.stem}")
    device = rk.resolve_device(args.device)
    try:
        from muscriptor import NoteEndEvent, NoteStartEvent, TranscriptionModel
    except ImportError as e:  # pragma: no cover
        rk.fail(f"muscriptor not installed: {e}")
    weights = os.path.join(rk.MODELS_DIR, "muscriptor", SIZE, "model.safetensors")
    if not os.path.exists(weights):
        rk.fail(f"muscriptor weights not baked at {weights} (gated on HF; accept the licence and rebuild with HF_TOKEN)")
    dtype = DTYPE if device.startswith("cuda") else None
    model = TranscriptionModel.load_model(weights, device=device, dtype=dtype)
    import torch

    audio, sr = rk.load_audio(args.input, sr=16000, mono=True)
    open_events: dict[int, object] = {}
    notes = []
    for ev in model.transcribe((torch.from_numpy(audio), sr), instruments=INSTRUMENTS[args.stem]):
        if isinstance(ev, NoteStartEvent):
            open_events[ev.index] = ev
        elif isinstance(ev, NoteEndEvent):
            s = open_events.pop(ev.start_event_index, None)
            if s is not None:
                notes.append({"onset": s.start_time, "offset": ev.end_time, "pitch": s.pitch, "velocity": 0.8})
    for s in open_events.values():  # never closed → short note
        notes.append({"onset": s.start_time, "offset": s.start_time + 0.1, "pitch": s.pitch, "velocity": 0.8})
    rk.write_json(args.output, rk.notes_result(args.stem, "muscriptor", f"muscriptor-{SIZE}", notes))


if __name__ == "__main__":
    main()
