import numpy as np
import pytest

from stemkit.analysis import estimate_bpm, estimate_key


def _click_track(bpm: float, sr: int = 22050, secs: float = 30.0) -> np.ndarray:
    y = np.zeros(int(sr * secs), dtype=np.float32)
    period = int(round(sr * 60 / bpm))
    for i in range(0, len(y) - 200, period):
        y[i:i + 200] += np.hanning(200) * np.random.default_rng(0).standard_normal(200) * 0.5
    return y


def test_bpm_fine_estimate_on_click_track():
    fine, coarse = estimate_bpm(_click_track(137.0), 22050)
    assert fine == pytest.approx(137.0, abs=0.3)


def test_key_on_a_minor_triad():
    sr = 22050
    t = np.arange(sr * 8) / sr
    y = sum(np.sin(2 * np.pi * f * t) for f in (220.0, 261.63, 329.63)).astype(np.float32) * 0.2  # A C E
    corr, label, idx, minor = estimate_key(y, sr)[0]
    assert label in ("A minor", "C major")  # relative keys share the pitch set; A minor expected to win
    assert corr > 0.6
