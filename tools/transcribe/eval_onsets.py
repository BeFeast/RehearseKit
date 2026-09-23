#!/usr/bin/env python3
"""Compare two notes.json files with mir_eval: onset F-measure (±50 ms) and
note F1 (onset + pitch, offsets ignored).

    eval_onsets.py reference.json estimate.json [--window 0.05]

Used for the S1 reference figure: the guitar adapter on the separated stem
(estimate) against the same adapter on the isolated guitar track / DI
(reference). Not a gate — it tells how much the separation costs.
"""
from __future__ import annotations

import argparse
import json

import numpy as np


def load(path: str):
    with open(path) as f:
        d = json.load(f)
    notes = d["notes"] if isinstance(d, dict) else d
    onsets = np.array([n["onset"] for n in notes], dtype=float)
    intervals = np.array([[n["onset"], max(n["offset"], n["onset"] + 0.01)] for n in notes], dtype=float).reshape(-1, 2)
    pitches = np.array([n["pitch"] for n in notes], dtype=float)
    return onsets, intervals, pitches


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("reference")
    p.add_argument("estimate")
    p.add_argument("--window", type=float, default=0.05)
    a = p.parse_args()
    import mir_eval

    ro, ri, rp = load(a.reference)
    eo, ei, ep = load(a.estimate)
    f, prec, rec = mir_eval.onset.f_measure(ro, eo, window=a.window)
    out = {"reference_notes": int(len(ro)), "estimate_notes": int(len(eo)),
           "onset": {"f": round(f, 4), "precision": round(prec, 4), "recall": round(rec, 4), "window_s": a.window}}
    if len(ro) and len(eo):
        P, R, F, _ = mir_eval.transcription.precision_recall_f1_overlap(
            ri, mir_eval.util.midi_to_hz(rp), ei, mir_eval.util.midi_to_hz(ep),
            onset_tolerance=a.window, pitch_tolerance=50.0, offset_ratio=None)
        out["note_onset_pitch"] = {"f": round(F, 4), "precision": round(P, 4), "recall": round(R, 4)}
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
