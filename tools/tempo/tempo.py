#!/usr/bin/env python3
"""Tempo analyser for the RehearseKit worker.

    tempo.py <source.wav>  ->  JSON on stdout

    {"bpm": 128.0 | null, "confidence": 0.0..1.0, "beats": [sec, ...],
     "raw_bpm": 128.0, "method": "librosa.beat_track"}

Ported from the legacy backend (app/services/audio.py detect_tempo), which
returned librosa.beat.beat_track's tempo unconditionally. This version adds a
confidence gate: `bpm` is null when the estimate is not trustworthy, because
a wrong tempo written into a DAW project is worse than none.

Confidence is the mean of two [0, 1] scores:

  consistency  fraction of frame-wise tempo estimates (librosa.feature.tempo
               with aggregate=None) that agree with the global tempo within
               4 %, counting octave errors (x0.5, x2) as agreement. Steady
               music scores close to 1; rubato, ambient or speech scatter.
  regularity   1 - min(1, CV / 0.25) where CV is the coefficient of variation
               of the inter-beat intervals from the beat tracker. A locked
               beat grid has CV near 0; a tracker that is guessing jitters.

The gate is 0.5 (see CONFIDENCE_THRESHOLD): with both terms weighted equally
a track has to be either very consistent or very regular to pass, and a
pure tone / noise file (no onsets) lands well below it.
"""
from __future__ import annotations

import json
import sys
import warnings

CONFIDENCE_THRESHOLD = 0.5
ANALYSIS_SR = 22050
TOLERANCE = 0.04


def analyse(path: str) -> dict:
    warnings.filterwarnings("ignore")
    import librosa
    import numpy as np

    y, sr = librosa.load(path, sr=ANALYSIS_SR, mono=True)
    if y.size < sr:  # under a second: nothing to track
        return {"bpm": None, "confidence": 0.0, "beats": [], "raw_bpm": None, "method": "librosa.beat_track"}

    onset_env = librosa.onset.onset_strength(y=y, sr=sr)
    tempo, beat_frames = librosa.beat.beat_track(onset_envelope=onset_env, sr=sr, units="frames")
    tempo = float(np.atleast_1d(tempo)[0])
    beats = librosa.frames_to_time(beat_frames, sr=sr)

    # Consistency of local tempo with the global estimate.
    local = librosa.feature.tempo(onset_envelope=onset_env, sr=sr, aggregate=None)
    local = np.asarray(local, dtype=float).ravel()
    local = local[local > 0]
    if tempo > 0 and local.size:
        ratio = local / tempo
        agree = np.zeros_like(ratio, dtype=bool)
        for k in (0.5, 1.0, 2.0):
            agree |= np.abs(ratio - k) <= TOLERANCE * k
        consistency = float(agree.mean())
    else:
        consistency = 0.0

    # Regularity of the beat grid.
    if beats.size >= 8:
        ibi = np.diff(beats)
        cv = float(np.std(ibi) / np.mean(ibi)) if np.mean(ibi) > 0 else 1.0
        regularity = max(0.0, 1.0 - min(1.0, cv / 0.25))
    else:
        regularity = 0.0

    # Silence or a pure tone yields an almost flat onset envelope; treat as no beat.
    if float(np.std(onset_env)) < 1e-3:
        consistency, regularity = 0.0, 0.0

    confidence = round(0.5 * consistency + 0.5 * regularity, 3)
    raw_bpm = round(tempo, 2) if tempo > 0 else None
    bpm = raw_bpm if raw_bpm is not None and confidence >= CONFIDENCE_THRESHOLD else None
    return {
        "bpm": bpm,
        "confidence": confidence,
        "beats": [round(float(t), 3) for t in beats],
        "raw_bpm": raw_bpm,
        "consistency": round(consistency, 3),
        "regularity": round(regularity, 3),
        "method": "librosa.beat_track",
    }


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: tempo.py <audio file>", file=sys.stderr)
        return 2
    try:
        result = analyse(argv[1])
    except Exception as exc:  # noqa: BLE001 - report everything to the worker
        print(f"tempo.py: {type(exc).__name__}: {exc}", file=sys.stderr)
        return 1
    json.dump(result, sys.stdout)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
