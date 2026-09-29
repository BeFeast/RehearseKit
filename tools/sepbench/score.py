#!/usr/bin/env python3
"""score.py PREP_DIR OUT_DIR — SDR and SI-SDR per config, song and stem against the prep references.

SDR is the plain utterance SDR used by MSST and the MVSEP leaderboards: 10·log10(Σref² / Σ(ref−est)²)
over the whole stereo track. SI-SDR rescales the estimate first, so a level mismatch does not count.
A silent reference (1928 has no vocals) is skipped. The mixes were built as the exact sum of the
references, so no alignment step is needed. Writes OUT_DIR/scores.json and prints a markdown table.
"""
import glob, json, os, sys

import numpy as np
import soundfile as sf

STEMS = ("vocals", "drums", "bass", "other")


def sdr(ref, est):
    return 10 * np.log10(np.sum(ref**2) / max(np.sum((ref - est) ** 2), 1e-12))


def si_sdr(ref, est):
    r, e = ref.ravel(), est.ravel()
    a = np.dot(e, r) / np.dot(r, r)
    return 10 * np.log10(np.sum((a * r) ** 2) / max(np.sum((a * r - e) ** 2), 1e-12))


def main(prep, out):
    scores = {}
    for cfg_dir in sorted(d for d in glob.glob(os.path.join(out, "*")) if os.path.isdir(d)):
        cfg = os.path.basename(cfg_dir)
        for song_dir in sorted(glob.glob(os.path.join(cfg_dir, "*"))):
            song = os.path.basename(song_dir)
            for s in STEMS:
                ref, _ = sf.read(os.path.join(prep, song, s + ".wav"), dtype="float64")
                if not np.any(ref):
                    continue
                est, _ = sf.read(os.path.join(song_dir, s + ".wav"), dtype="float64")
                n = min(len(ref), len(est))
                scores.setdefault(cfg, {}).setdefault(song, {})[s] = {
                    "sdr": round(sdr(ref[:n], est[:n]), 2), "si_sdr": round(si_sdr(ref[:n], est[:n]), 2)}
    with open(os.path.join(out, "scores.json"), "w") as f:
        json.dump(scores, f, indent=1)
    songs = sorted({s for c in scores.values() for s in c})
    for metric in ("sdr", "si_sdr"):
        print(f"\n{metric.upper()} dB")
        cols = [(song, s) for song in songs for s in STEMS if any(s in scores[c].get(song, {}) for c in scores)]
        print("| config | " + " | ".join(f"{song} {s}" for song, s in cols) + " | mean d/b/o |")
        print("|---" * (len(cols) + 2) + "|")
        for cfg, per in scores.items():
            vals = [per.get(song, {}).get(s, {}).get(metric) for song, s in cols]
            dbo = [per[song][s][metric] for song in per for s in ("drums", "bass", "other") if s in per[song]]
            print(f"| {cfg} | " + " | ".join("" if v is None else f"{v:.2f}" for v in vals) + f" | {np.mean(dbo):.2f} |")


if __name__ == "__main__":
    main(*sys.argv[1:3])
