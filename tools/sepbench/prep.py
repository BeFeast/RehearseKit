#!/usr/bin/env python3
"""prep.py SONGS.json IN_DIR OUT_DIR — multitrack stems → 44.1 kHz float references + their sum as the mix.

SONGS.json maps a song folder to glob lists per target, e.g.
  {"raindrops": {"vocals": ["*Vocal*", "*Voice*"], "drums": ["*Drums*"], "bass": ["*Bass*"],
                 "other": ["*Piano*", "*Chords*"]}}
Every stem file in the folder must match exactly one target (unmatched files fail the run, so a
forgotten track cannot leak into the mix without a reference). The mix is the plain sum, scaled with
the references so its peak sits at 0.9: the separators see the same material the scores are taken on.
"""
import fnmatch, glob, json, os, sys

import numpy as np
import soundfile as sf
from scipy.signal import resample_poly

SR = 44100
TARGETS = ("vocals", "drums", "bass", "other")


def load(path):
    x, sr = sf.read(path, always_2d=True, dtype="float64")
    if x.shape[1] == 1:
        x = np.repeat(x, 2, axis=1)
    if sr != SR:
        g = np.gcd(sr, SR)
        x = resample_poly(x, SR // g, sr // g, axis=0)
    return x


def main(songs_path, in_dir, out_dir):
    songs = json.load(open(songs_path))
    for song, spec in songs.items():
        files = sorted(glob.glob(os.path.join(in_dir, song, "*.wav")) + glob.glob(os.path.join(in_dir, song, "*.flac")))
        ignore = spec.pop("ignore", [])
        refs, used = {}, set()
        for f in files:
            name = os.path.basename(f)
            if any(fnmatch.fnmatch(name, p) for p in ignore):
                continue
            hits = [t for t in TARGETS if any(fnmatch.fnmatch(name, p) for p in spec.get(t, []))]
            if len(hits) != 1:
                sys.exit(f"{song}/{name}: matches {hits or 'no target'}")
            x = load(f)
            t = hits[0]
            refs[t] = x if t not in refs else _add(refs[t], x)
            used.add(name)
        n = max(len(v) for v in refs.values())
        for t in TARGETS:
            refs[t] = np.pad(refs[t], ((0, n - len(refs[t])), (0, 0))) if t in refs else np.zeros((n, 2))
        mix = sum(refs.values())
        gain = 0.9 / np.abs(mix).max()
        dst = os.path.join(out_dir, song)
        os.makedirs(dst, exist_ok=True)
        sf.write(os.path.join(dst, "mix.wav"), (mix * gain).astype(np.float32), SR, subtype="FLOAT")
        for t, x in refs.items():
            sf.write(os.path.join(dst, t + ".wav"), (x * gain).astype(np.float32), SR, subtype="FLOAT")
        print(f"{song}: {n / SR:.1f} s, {len(used)} stems, gain {20 * np.log10(gain):+.1f} dB, "
              f"silent: {[t for t in TARGETS if not np.any(refs[t])]}")


def _add(a, b):
    n = max(len(a), len(b))
    return np.pad(a, ((0, n - len(a)), (0, 0))) + np.pad(b, ((0, n - len(b)), (0, 0)))


if __name__ == "__main__":
    main(*sys.argv[1:4])
