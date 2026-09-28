#!/usr/bin/env python3
"""Synthetic beat-grid reference: a drum-machine track with known beats.

    synth_click.py OUT.wav OUT.json

Tempo and meter changes the grid must follow (beat = quarter note):
  8 bars 4/4 at 100, 8 bars 4/4 at 128 (step), 8 bars 4/4 ramping 128→96,
  6 bars 3/4 at 96, 4 bars 7/4 then 4 bars 5/4 at 110, 8 bars 4/4 at 110.
Kick + bass note on every downbeat, snare on the backbeats of 4/4 and 3/4
(beat 3 of odd bars), closed hat on every beat and eighth. 1.3 s of
silence before the first beat, 2 s after the last.

OUT.json: {"name", "duration", "ref_beats", "ref_downbeats", "sections"}.
The beat tracker output is merged in later (grid_beatthis.py on OUT.wav).
"""
from __future__ import annotations

import json
import sys

import numpy as np
import soundfile as sf

SR = 44100
LEAD = 1.3
TAIL = 2.0

# (bars, numerator, bpm at the first beat, bpm at the last beat)
SECTIONS = [
    (8, 4, 100, 100),
    (8, 4, 128, 128),
    (8, 4, 128, 96),
    (6, 3, 96, 96),
    (4, 7, 110, 110),
    (4, 5, 110, 110),
    (8, 4, 110, 110),
]


def schedule():
    beats, downs, pos = [], [], []  # pos: (index in bar, numerator)
    t = LEAD
    for bars, num, b0, b1 in SECTIONS:
        n = bars * num
        for i in range(n):
            bpm = b0 + (b1 - b0) * i / max(n - 1, 1)
            if i % num == 0:
                downs.append(t)
            beats.append(t)
            pos.append((i % num, num))
            t += 60 / bpm
    return beats, downs, pos, t  # t: where the next downbeat would fall


def env(n, tau):
    return np.exp(-np.arange(n) / (tau * SR))


def kick():
    n = int(0.35 * SR)
    ts = np.arange(n) / SR
    f = 50 + 90 * np.exp(-ts / 0.04)
    return 0.9 * np.sin(2 * np.pi * np.cumsum(f) / SR) * env(n, 0.12)


def snare(rng):
    n = int(0.2 * SR)
    ts = np.arange(n) / SR
    return (0.35 * rng.standard_normal(n) + 0.3 * np.sin(2 * np.pi * 190 * ts)) * env(n, 0.05)


def hat(rng, gain):
    n = int(0.05 * SR)
    x = rng.standard_normal(n)
    x = np.diff(np.r_[0.0, x])  # crude high-pass
    return gain * x * env(n, 0.012)


def bass(freq, dur):
    n = int(dur * SR)
    ts = np.arange(n) / SR
    return 0.25 * np.tanh(2 * np.sin(2 * np.pi * freq * ts)) * env(n, dur / 2)


def main() -> None:
    out_wav, out_json = sys.argv[1], sys.argv[2]
    rng = np.random.default_rng(7)
    beats, downs, pos, end = schedule()
    dur = end + TAIL
    y = np.zeros(int(dur * SR) + SR)

    def add(t, x):
        i = int(round(t * SR))
        y[i:i + len(x)] += x

    k, notes = kick(), [41.2, 55.0, 49.0, 36.7]
    for j, (t, (i, num)) in enumerate(zip(beats, pos)):
        nxt = beats[j + 1] if j + 1 < len(beats) else end
        add(t, hat(rng, 0.25))
        add((t + nxt) / 2, hat(rng, 0.12))
        if i == 0:
            add(t, k)
            add(t, bass(notes[(len([d for d in downs if d <= t]) - 1) % 4], 0.8 * (nxt - t) * 2))
        elif (num in (3, 4) and i % 2 == 1) or (num > 4 and i == 2):
            add(t, snare(rng))
    y = 0.8 * y / np.abs(y).max()
    sf.write(out_wav, y[: int(dur * SR)], SR, subtype="PCM_24")
    with open(out_json, "w") as f:
        json.dump({
            "name": "click",
            "duration": round(dur, 4),
            "ref_beats": [round(b, 6) for b in beats],
            "ref_downbeats": [round(d, 6) for d in downs],
            "sections": [{"bars": b, "numerator": n, "bpm": [b0, b1]} for b, n, b0, b1 in SECTIONS],
        }, f)
        f.write("\n")


if __name__ == "__main__":
    main()
