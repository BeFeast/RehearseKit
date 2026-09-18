"""Write a .dawproject (Bitwig-native; also read by Studio One, Cubase 14+) with one audio track per stem.

Warp points map the file 1:1 (beats = seconds * bpm / 60) so nothing is stretched at project tempo.
Neither DAWproject nor Bitwig has a project-level key: it goes into metadata Comment and clip names.
"""
from __future__ import annotations

import zipfile
from pathlib import Path
from xml.sax.saxutils import escape as e

from . import STEMS
from .download import probe

COLORS = {"drums": "#d35f5f", "bass": "#5f8fd3", "guitar": "#d3a05f", "other": "#8f8f8f", "vocals": "#5fd38f"}
ORDER = ("drums", "bass", "guitar", "other", "vocals")


class _Ids:
    def __init__(self):
        self.n = 0

    def __call__(self) -> str:
        self.n += 1
        return f"id{self.n}"


def _fmt_bpm(bpm: float) -> str:
    return str(int(bpm)) if float(bpm).is_integer() else f"{bpm:g}"


def build_project_xml(stems: dict[str, Path], bpm: float, key_short: str, app_name: str = "stemkit") -> tuple[str, list[tuple[Path, str]]]:
    nid = _Ids()
    tempo_id, ts_id = nid(), nid()
    master_id, master_ch = nid(), nid()
    tracks, lanes, files = [], [], []
    for s in ORDER:
        path = stems[s]
        p = probe(path)
        secs = p["frames"] / p["sample_rate"]
        beats = secs * bpm / 60.0
        tid, cid = nid(), nid()
        tracks.append(
            f'    <Track contentType="audio" loaded="true" id="{tid}" name="{e(s.capitalize())}" color="{COLORS[s]}">\n'
            f'      <Channel audioChannels="2" destination="{master_ch}" role="regular" solo="false" id="{cid}">\n'
            f'        <Mute value="false" id="{nid()}" name="Mute"/>\n'
            f'        <Pan max="1.000000" min="0.000000" unit="normalized" value="0.500000" id="{nid()}" name="Pan"/>\n'
            f'        <Volume max="2.000000" min="0.000000" unit="linear" value="1.000000" id="{nid()}" name="Volume"/>\n'
            f'      </Channel>\n    </Track>')
        arc = f"audio/{path.name}"
        files.append((path, arc))
        clip_name = f"{s} [{_fmt_bpm(bpm)}bpm {key_short}]"
        d = f"{beats:.10f}"
        lanes.append(
            f'      <Lanes track="{tid}" id="{nid()}">\n        <Clips id="{nid()}">\n'
            f'          <Clip time="0.0" duration="{d}" playStart="0.0" loopStart="0.0" loopEnd="{d}" '
            f'fadeTimeUnit="beats" fadeInTime="0.0" fadeOutTime="0.0" name="{e(clip_name)}">\n'
            f'            <Clips id="{nid()}">\n'
            f'              <Clip time="0.0" duration="{d}" contentTimeUnit="beats" playStart="0.0" '
            f'fadeTimeUnit="beats" fadeInTime="0.0" fadeOutTime="0.0">\n'
            f'                <Warps contentTimeUnit="seconds" timeUnit="beats" id="{nid()}">\n'
            f'                  <Audio algorithm="stretch" channels="{p["channels"]}" duration="{secs:.10f}" '
            f'sampleRate="{p["sample_rate"]}" id="{nid()}">\n'
            f'                    <File path="{e(arc)}"/>\n                  </Audio>\n'
            f'                  <Warp time="0.0" contentTime="0.0"/>\n'
            f'                  <Warp time="{d}" contentTime="{secs:.10f}"/>\n'
            f'                </Warps>\n              </Clip>\n            </Clips>\n          </Clip>\n'
            f'        </Clips>\n      </Lanes>')
    nl = "\n"
    xml = (
        '<?xml version="1.0" encoding="UTF-8"?>\n<Project version="1.0">\n'
        f'  <Application name="{e(app_name)}" version="1.0"/>\n  <Transport>\n'
        f'    <Tempo max="666.000000" min="20.000000" unit="bpm" value="{bpm:.6f}" id="{tempo_id}" name="Tempo"/>\n'
        f'    <TimeSignature denominator="4" numerator="4" id="{ts_id}"/>\n  </Transport>\n  <Structure>\n'
        f'{nl.join(tracks)}\n'
        f'    <Track contentType="audio notes" loaded="true" id="{master_id}" name="Master">\n'
        f'      <Channel audioChannels="2" role="master" solo="false" id="{master_ch}">\n'
        f'        <Mute value="false" id="{nid()}" name="Mute"/>\n'
        f'        <Pan max="1.000000" min="0.000000" unit="normalized" value="0.500000" id="{nid()}" name="Pan"/>\n'
        f'        <Volume max="2.000000" min="0.000000" unit="linear" value="1.000000" id="{nid()}" name="Volume"/>\n'
        f'      </Channel>\n    </Track>\n  </Structure>\n'
        f'  <Arrangement id="{nid()}">\n    <Lanes timeUnit="beats" id="{nid()}">\n{nl.join(lanes)}\n    </Lanes>\n'
        '  </Arrangement>\n  <Scenes/>\n</Project>\n'
    )
    return xml, files


def build_metadata_xml(title: str, comment: str) -> str:
    return ('<?xml version="1.0" encoding="UTF-8"?>\n<MetaData>\n'
            f'  <Title>{e(title)}</Title>\n  <Comment>{e(comment)}</Comment>\n</MetaData>\n')


def write_dawproject(stems: dict[str, Path], out: Path, bpm: float, key_short: str, title: str, comment: str = "") -> Path:
    missing = [s for s in STEMS if s not in stems]
    if missing:
        raise ValueError(f"missing stems: {missing}")
    xml, files = build_project_xml(stems, bpm, key_short)
    out.parent.mkdir(parents=True, exist_ok=True)
    # audio is already compressed or incompressible float PCM: store, don't deflate
    with zipfile.ZipFile(out, "w", zipfile.ZIP_STORED) as z:
        z.writestr("project.xml", xml)
        z.writestr("metadata.xml", build_metadata_xml(title, comment or f"{_fmt_bpm(bpm)} BPM, {key_short}"))
        for path, arc in files:
            z.write(path, arc)
    return out
