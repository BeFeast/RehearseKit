"""Tempo and key estimation (librosa). Estimates, not ground truth — surface them as such."""
from __future__ import annotations

from dataclasses import dataclass, asdict
from pathlib import Path

import numpy as np

NOTES = ["C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"]
# Krumhansl-Schmuckler key profiles
_MAJ = np.array([6.35, 2.23, 3.48, 2.33, 4.38, 4.09, 2.52, 5.19, 2.39, 3.66, 2.29, 2.88])
_MIN = np.array([6.33, 2.68, 3.52, 5.38, 2.60, 3.53, 2.54, 4.75, 3.98, 2.69, 3.34, 3.17])
# Camelot wheel: (note index, is_minor) -> code
_CAMELOT_MAJOR = {0: "8B", 1: "3B", 2: "10B", 3: "5B", 4: "12B", 5: "7B", 6: "2B", 7: "9B", 8: "4B", 9: "11B", 10: "6B", 11: "1B"}
_CAMELOT_MINOR = {0: "5A", 1: "12A", 2: "7A", 3: "2A", 4: "9A", 5: "4A", 6: "11A", 7: "6A", 8: "1A", 9: "8A", 10: "3A", 11: "10A"}


@dataclass
class Analysis:
    bpm: float
    bpm_coarse: float
    key: str            # e.g. "C# minor"
    key_short: str      # e.g. "C#m"
    camelot: str
    key_confidence: float
    key_runner_up: str
    sections: list      # [(start_sec, key, confidence)]

    def to_dict(self) -> dict:
        return asdict(self)


def estimate_key(y: np.ndarray, sr: int) -> list[tuple[float, str, int, bool]]:
    """Return [(corr, label, note_idx, is_minor)] sorted best-first."""
    import librosa

    yh = librosa.effects.harmonic(y)
    chroma = librosa.feature.chroma_cqt(y=yh, sr=sr).mean(axis=1)
    out = []
    for i in range(12):
        out.append((float(np.corrcoef(np.roll(_MAJ, i), chroma)[0, 1]), f"{NOTES[i]} major", i, False))
        out.append((float(np.corrcoef(np.roll(_MIN, i), chroma)[0, 1]), f"{NOTES[i]} minor", i, True))
    out.sort(reverse=True)
    return out


def estimate_bpm(y: np.ndarray, sr: int, lo: float = 60.0, hi: float = 200.0) -> tuple[float, float]:
    """(fine, coarse). Coarse = librosa tempo estimator (resolution ~1 BPM).
    Fine = grid search on the onset autocorrelation summed at 1,2,4,8,16 beat multiples (0.05 BPM steps)."""
    import librosa

    hop = 64
    onset = librosa.onset.onset_strength(y=y, sr=sr, hop_length=hop)
    coarse = float(np.atleast_1d(librosa.feature.tempo(onset_envelope=onset, sr=sr, hop_length=hop))[0])
    fr = sr / hop
    ac = librosa.autocorrelate(onset, max_size=int(fr * 60 / lo * 16) + 2)

    def score(t: float) -> float:
        per = fr * 60.0 / t
        return sum(ac[int(round(per * k))] for k in (1, 2, 4, 8, 16) if int(round(per * k)) < len(ac))

    # search around the coarse estimate (and its half/double) to avoid octave errors elsewhere
    cands = [c for c in (coarse, coarse / 2, coarse * 2) if lo <= c <= hi]
    best_t, best_s = coarse, -1.0
    for c in cands:
        for t in np.arange(max(lo, c - 4), min(hi, c + 4), 0.05):
            s = score(float(t))
            if s > best_s:
                best_s, best_t = s, float(t)
    return round(best_t, 2), round(coarse, 1)


def analyze(path: Path, section_sec: int = 60) -> Analysis:
    import librosa

    y, sr = librosa.load(str(path), sr=22050, mono=True)
    bpm, coarse = estimate_bpm(y, sr)
    ranked = estimate_key(y, sr)
    corr, label, idx, minor = ranked[0]
    short = NOTES[idx] + ("m" if minor else "")
    camelot = (_CAMELOT_MINOR if minor else _CAMELOT_MAJOR)[idx]
    sections = []
    step = section_sec * sr
    for i in range(0, len(y), step):
        seg = y[i:i + step]
        if len(seg) < 10 * sr:
            break
        c, l, _, _ = estimate_key(seg, sr)[0]
        sections.append((i / sr, l, round(c, 2)))
    return Analysis(bpm=bpm, bpm_coarse=coarse, key=label, key_short=short, camelot=camelot,
                    key_confidence=round(corr, 3), key_runner_up=ranked[1][1], sections=sections)
