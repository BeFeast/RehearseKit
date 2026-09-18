import subprocess
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path

import pytest

from stemkit import STEMS
from stemkit.dawproject import write_dawproject


@pytest.fixture
def stems(tmp_path: Path) -> dict[str, Path]:
    out = {}
    for s in STEMS:
        p = tmp_path / f"{s}.wav"
        # 3 s of silence, 44.1 kHz stereo float
        subprocess.run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "3",
                        "-c:a", "pcm_f32le", str(p)], check=True)
        out[s] = p
    return out


def test_dawproject_structure(stems, tmp_path):
    out = write_dawproject(stems, tmp_path / "x.dawproject", 137.0, "C#m", "Song")
    with zipfile.ZipFile(out) as z:
        names = z.namelist()
        assert "project.xml" in names and "metadata.xml" in names
        assert sorted(n for n in names if n.startswith("audio/")) == sorted(f"audio/{s}.wav" for s in STEMS)
        root = ET.fromstring(z.read("project.xml"))
    assert root.find("Transport/Tempo").get("value") == "137.000000"
    tracks = root.findall("Structure/Track")
    assert [t.get("name") for t in tracks] == ["Drums", "Bass", "Guitar", "Other", "Vocals", "Master"]
    master_ch = tracks[-1].find("Channel").get("id")
    assert all(t.find("Channel").get("destination") == master_ch for t in tracks[:-1])
    # warp maps 3 s onto 3*137/60 beats, 1:1 at project tempo
    warps = root.findall(".//Warps")
    assert len(warps) == 5
    for w in warps:
        last = w.findall("Warp")[-1]
        assert float(last.get("time")) == pytest.approx(3 * 137 / 60, rel=1e-6)
        assert float(last.get("contentTime")) == pytest.approx(3.0, rel=1e-6)
    clip = root.find(".//Lanes/Lanes/Clips/Clip")
    assert clip.get("name") == "drums [137bpm C#m]"


def test_missing_stem_raises(stems, tmp_path):
    del stems["guitar"]
    with pytest.raises(ValueError, match="guitar"):
        write_dawproject(stems, tmp_path / "x.dawproject", 120, "Am", "Song")
