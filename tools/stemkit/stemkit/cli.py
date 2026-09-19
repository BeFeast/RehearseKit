"""stemkit CLI.

  stemkit run <youtube-url | audio-file> --out DIR [--bpm 137] [--key C#m] [--keep-instance] [--local-stems DIR]
  stemkit analyze <audio-file>
  stemkit dawproject --stems DIR --bpm 137 --key C#m --title NAME --out FILE.dawproject
  stemkit offers

Output layout of `run` (mirrors what was hand-built for the first track):
  <out>/<title>/source/      original download + 44.1k FLAC fed to the models
  <out>/<title>/stems/       vocals/drums/bass/guitar/other  (32-bit float WAV, sum == mix)
  <out>/<title>/bitwig/      <title> [<bpm>bpm <key>].dawproject
  <out>/<title>/analysis.json
"""
from __future__ import annotations

import argparse
import json
import re
import shutil
import sys
import time
from pathlib import Path

from . import STEMS
from . import dawproject, download, remote, vast


def _log(*a):
    print("[stemkit]", *a, file=sys.stderr, flush=True)


def _safe_title(s: str) -> str:
    return re.sub(r"[\\/:*?\"<>|]+", "-", s).strip() or "track"


def cmd_offers(args) -> int:
    for o in vast.search_offers(limit=args.limit):
        print(f"{o.id}\t${o.dph:.3f}/h\t{o.gpu}\t{o.geo}\tdown {o.inet_down:.0f} Mb/s\trel {o.reliability:.3f}")
    return 0


def cmd_analyze(args) -> int:
    from .analysis import analyze
    a = analyze(Path(args.audio))
    print(json.dumps(a.to_dict(), indent=1))
    return 0


def cmd_dawproject(args) -> int:
    stems = {s: Path(args.stems) / f"{s}.wav" for s in STEMS}
    out = dawproject.write_dawproject(stems, Path(args.out), args.bpm, args.key, args.title)
    print(out)
    return 0


def cmd_run(args) -> int:
    out_root = Path(args.out)
    t0 = time.time()

    # 1. acquire
    if download.is_youtube_url(args.source):
        tmp = out_root / "_download"
        src, info = download.download_youtube(args.source, tmp)
        title = _safe_title(args.title or info.get("title") or src.stem)
    else:
        src = Path(args.source)
        info = {}
        title = _safe_title(args.title or src.stem)
    proj = out_root / title
    source_dir, stems_dir, daw_dir = proj / "source", proj / "stems", proj / "bitwig"
    source_dir.mkdir(parents=True, exist_ok=True)
    if download.is_youtube_url(args.source):
        src = Path(shutil.move(str(src), source_dir / src.name))  # our own download: move
        shutil.rmtree(out_root / "_download", ignore_errors=True)
    elif src.resolve().parent != source_dir.resolve():
        src = Path(shutil.copy2(src, source_dir / src.name))      # user's file: never move it
    mix = source_dir / "mix.flac"
    if src.resolve() == mix.resolve():
        mix = source_dir / "mix.44k.flac"
    download.to_flac_44k(src, mix)
    _log(f"source: {src.name} ({download.probe(src)['codec']}) -> {mix.name}")

    # 2. analyze (bpm/key) — local CPU, runs while the GPU box boots if we had threads; keep it simple and sequential
    bpm, key_short, analysis = args.bpm, args.key, None
    if bpm is None or key_short is None:
        from .analysis import analyze
        analysis = analyze(mix)
        bpm = bpm if bpm is not None else analysis.bpm
        key_short = key_short if key_short is not None else analysis.key_short
        _log(f"analysis: {analysis.bpm} BPM (coarse {analysis.bpm_coarse}), {analysis.key} "
             f"(r={analysis.key_confidence}, runner-up {analysis.key_runner_up})")
    if args.round_bpm:
        bpm = float(round(bpm))

    # 3. separate — on a rented GPU unless stems were supplied
    if args.local_stems:
        stems_dir.mkdir(parents=True, exist_ok=True)
        for s in STEMS:
            shutil.copy2(Path(args.local_stems) / f"{s}.wav", stems_dir / f"{s}.wav")
    else:
        offers = vast.search_offers(args.query or vast.DEFAULT_QUERY, limit=5)
        if not offers:
            _log("no vast.ai offers match the query")
            return 2
        offer = offers[0]
        _log(f"renting offer {offer.id}: {offer.gpu} {offer.geo} ${offer.dph:.3f}/h (credit ${vast.credit():.2f})")
        inst = vast.create_instance(offer.id, label=f"stemkit {title[:40]}")
        try:
            vast.wait_running(inst)
            _log(f"instance {inst.id} up at {inst.host}:{inst.port}")
            remote.upload(inst, mix)
            remote.run_separation(inst)
            remote.download(inst, stems_dir)
        finally:
            if args.keep_instance:
                _log(f"KEEPING instance {inst.id} (--keep-instance) — destroy it yourself: vastai destroy instance {inst.id} -y")
            else:
                vast.destroy_instance(inst)
                _log(f"instance {inst.id} destroyed")
    missing = [s for s in STEMS if not (stems_dir / f"{s}.wav").exists()]
    if missing:
        _log(f"stems missing after separation: {missing}")
        return 3

    # 4. name stems and build the .dawproject
    tag = f"[{dawproject._fmt_bpm(bpm)}bpm {key_short}]"
    stems = {}
    for s in STEMS:
        dst = stems_dir / f"{title} - {s} {tag}.wav"
        (stems_dir / f"{s}.wav").rename(dst)
        stems[s] = dst
    comment = f"{dawproject._fmt_bpm(bpm)} BPM, key {key_short} (librosa estimate). Source: {args.source}. Sum of stems == mix."
    daw = dawproject.write_dawproject(stems, daw_dir / f"{title} {tag}.dawproject", bpm, key_short, title, comment)
    (proj / "analysis.json").write_text(json.dumps({
        "source": args.source, "title": title, "bpm": bpm, "key": key_short,
        "analysis": analysis.to_dict() if analysis else None, "youtube": info or None,
        "stems": {s: p.name for s, p in stems.items()}, "dawproject": daw.name,
        "elapsed_sec": round(time.time() - t0),
    }, indent=1, ensure_ascii=False))
    _log(f"done in {round(time.time() - t0)}s -> {daw}")
    print(daw)
    return 0


def main(argv=None) -> int:
    p = argparse.ArgumentParser(prog="stemkit", description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)

    r = sub.add_parser("run", help="full pipeline")
    r.add_argument("source", help="YouTube URL or local audio file")
    r.add_argument("--out", required=True, help="output root directory")
    r.add_argument("--title", help="override project title")
    r.add_argument("--bpm", type=float, help="skip tempo detection")
    r.add_argument("--key", help="skip key detection, e.g. C#m or A")
    r.add_argument("--round-bpm", action="store_true", help="round detected BPM to an integer")
    r.add_argument("--query", help="vast.ai offer query (default: cheap reliable RTX 4090)")
    r.add_argument("--keep-instance", action="store_true", help="do not destroy the GPU instance afterwards")
    r.add_argument("--local-stems", help="skip the GPU step; dir with vocals/drums/bass/guitar/other.wav")
    r.set_defaults(fn=cmd_run)

    a = sub.add_parser("analyze", help="print BPM/key estimate as JSON")
    a.add_argument("audio")
    a.set_defaults(fn=cmd_analyze)

    d = sub.add_parser("dawproject", help="build a .dawproject from existing stems")
    d.add_argument("--stems", required=True)
    d.add_argument("--bpm", type=float, required=True)
    d.add_argument("--key", required=True)
    d.add_argument("--title", required=True)
    d.add_argument("--out", required=True)
    d.set_defaults(fn=cmd_dawproject)

    o = sub.add_parser("offers", help="list matching vast.ai offers")
    o.add_argument("--limit", type=int, default=10)
    o.set_defaults(fn=cmd_offers)

    args = p.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
