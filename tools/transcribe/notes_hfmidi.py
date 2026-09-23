#!/usr/bin/env python3
"""Guitar / bass / piano transcription with the xavriley fine-tunes of the
bytedance piano-transcription CRNN (MIT; hf_midi_transcription's models):
stem.wav → notes.json.

Checkpoints (xavriley/midi-transcription-models on Hugging Face, not gated)
are expected under $RK_MODELS_DIR/hf_midi/<file>.pth. All three are plain
"Regress_onset_offset_frame_velocity_CRNN" checkpoints (the piano one too,
so the wrapper's Note_pedal class is not used). Trained on solo/clean
material, so distorted electric guitar and busy bass come out rough — this
is the fallback when MuScriptor's gated weights are not available.

Thresholds: RK_HFMIDI_ONSET_THRESHOLD (0.3), RK_HFMIDI_FRAME_THRESHOLD (0.1).
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
ONSET = float(os.environ.get("RK_HFMIDI_ONSET_THRESHOLD", "0.3"))
FRAME = float(os.environ.get("RK_HFMIDI_FRAME_THRESHOLD", "0.1"))
SAMPLE_RATE = 16000


def main() -> None:
    args = rk.parse_args("notes")
    if args.stem not in CHECKPOINTS:
        rk.fail(f"hf_midi has no model for {args.stem}")
    device = rk.resolve_device(args.device)
    try:
        from piano_transcription_inference import PianoTranscription
    except ImportError as e:  # pragma: no cover
        rk.fail(f"piano_transcription_inference (hf_midi_transcription) not installed: {e}")
    ckpt_file = CHECKPOINTS[args.stem]
    ckpt = os.path.join(rk.MODELS_DIR, "hf_midi", ckpt_file)
    if not os.path.exists(ckpt):
        rk.fail(f"checkpoint {ckpt} not baked into the image")
    # The library prints progress on stdout; that is fine (stdout is ignored).
    model = PianoTranscription(
        "Regress_onset_offset_frame_velocity_CRNN", device=device, checkpoint_path=ckpt,
        segment_samples=10 * SAMPLE_RATE, batch_size=8,
        onset_threshold=ONSET, offset_threshold=ONSET, frame_threshold=FRAME, pedal_offset_threshold=0.2,
    )
    audio, _ = rk.load_audio(args.input, sr=SAMPLE_RATE, mono=True)
    tmp_mid = args.output + ".mid"
    try:
        res = model.transcribe(audio, tmp_mid)
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
