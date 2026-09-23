#!/usr/bin/env python3
"""Guitar / bass / piano transcription with xavriley/hf_midi_transcription
(MIT; bytedance CRNN fine-tunes): stem.wav → notes.json.

Checkpoints (xavriley/midi-transcription-models on Hugging Face, not gated)
are expected under $RK_MODELS_DIR/hf_midi/<file>.pth; otherwise they are
fetched through the HF cache. Trained on solo/clean material, so distorted
electric guitar comes out rough — this is the fallback when MuScriptor's
gated weights are not available.
"""
from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rk_adapter as rk  # noqa: E402

CHECKPOINTS = {
    "guitar": "guitar-gaps.pth",
    "bass": "filobass_20000_iterations.pth",
    "piano": "piano.pth",
}


def main() -> None:
    args = rk.parse_args("notes")
    if args.stem not in CHECKPOINTS:
        rk.fail(f"hf_midi has no model for {args.stem}")
    device = rk.resolve_device(args.device)
    try:
        from hf_midi_transcription import MidiTranscriptionModel
    except ImportError as e:  # pragma: no cover
        rk.fail(f"hf_midi_transcription not installed: {e}")
    ckpt_file = CHECKPOINTS[args.stem]
    local = os.path.join(rk.MODELS_DIR, "hf_midi", ckpt_file)
    ckpt = local if os.path.exists(local) else ckpt_file
    model = MidiTranscriptionModel(instrument=args.stem, checkpoint_path=ckpt, device=device, batch_size=8)
    audio, _ = rk.load_audio(args.input, sr=16000, mono=True)
    tmp_mid = args.output + ".mid"
    try:
        res = model.transcriptor.transcribe(audio, tmp_mid)
    finally:
        if os.path.exists(tmp_mid):
            os.remove(tmp_mid)
    notes = [
        {"onset": e["onset_time"], "offset": e["offset_time"], "pitch": e["midi_note"], "velocity": e["velocity"] / 127.0}
        for e in res["est_note_events"]
    ]
    rk.write_json(args.output, rk.notes_result(args.stem, "hfmidi", os.path.splitext(ckpt_file)[0], notes))


if __name__ == "__main__":
    main()
