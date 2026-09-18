"""Acquire the source audio (yt-dlp) and prepare the separation input (44.1 kHz FLAC)."""
from __future__ import annotations

import json
import re
import subprocess
from pathlib import Path

YT_RE = re.compile(r"^(https?://)?(www\.|music\.|m\.)?(youtube\.com|youtu\.be)/", re.I)


def is_youtube_url(s: str) -> bool:
    return bool(YT_RE.match(s))


def download_youtube(url: str, out_dir: Path) -> tuple[Path, dict]:
    """Download the best audio-only stream without re-encoding. Returns (path, info)."""
    out_dir.mkdir(parents=True, exist_ok=True)
    tmpl = str(out_dir / "%(title)s [%(id)s].%(ext)s")
    proc = subprocess.run(
        [
            "yt-dlp", "--no-playlist", "-f", "bestaudio", "-S", "abr,acodec",
            "-o", tmpl, "--no-simulate", "--print", "after_move:filepath", "--print", "%(.{id,title,duration})j",
            url,
        ],
        check=True, capture_output=True, text=True,
    )
    # --print order is not guaranteed relative to the after_move hook: classify lines by shape
    lines = [l for l in proc.stdout.splitlines() if l.strip()]
    info = next((json.loads(l) for l in lines if l.startswith("{")), {})
    path = next((Path(l) for l in reversed(lines) if not l.startswith("{") and Path(l).exists()), None)
    if path is None:
        raise RuntimeError(f"yt-dlp did not report an output file:\n{proc.stdout}")
    return path, info


def to_flac_44k(src: Path, dst: Path) -> Path:
    """Models are trained at 44.1 kHz; feed them exactly that, 24-bit, lossless."""
    dst.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        ["ffmpeg", "-v", "error", "-y", "-i", str(src), "-vn", "-c:a", "flac", "-sample_fmt", "s32", "-ar", "44100", str(dst)],
        check=True,
    )
    return dst


def probe(path: Path) -> dict:
    out = subprocess.run(
        ["ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries",
         "stream=codec_name,sample_rate,channels,duration_ts,bit_rate", "-of", "json", str(path)],
        check=True, capture_output=True, text=True,
    ).stdout
    s = json.loads(out)["streams"][0]
    return {"codec": s.get("codec_name"), "sample_rate": int(s["sample_rate"]), "channels": int(s["channels"]),
            "frames": int(s.get("duration_ts", 0)), "bit_rate": int(s.get("bit_rate", 0) or 0)}
