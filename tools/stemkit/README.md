# stemkit

YouTube URL (or audio file) → yt-dlp → 5 stems on a rented vast.ai RTX 4090 → `.dawproject` with tempo and key.

Standalone CLI, no Celery/DB dependency. It is the reference implementation of the separation
pipeline RehearseKit's GPU worker should converge on (replacing `htdemucs_6s`); the Celery task can
call `stemkit.remote` / `stemkit.dawproject` directly later.

## Pipeline

| Step | Where | What |
|---|---|---|
| acquire | local | `yt-dlp -f bestaudio` (no re-encode) → ffmpeg → 44.1 kHz/24-bit FLAC (models are trained at 44.1k) |
| analyze | local CPU | BPM: onset autocorrelation, 0.05 BPM grid around librosa's coarse estimate. Key: Krumhansl-Schmuckler on harmonic chroma + per-minute sections |
| separate | vast.ai GPU | `remote/separate.sh`: MelBand Roformer *Kim* vocals → instrumental → **BS-Rofo-SW** 6-stem BS-RoFormer (TTA) → fold piano/residuals into `other`. The five stems sum to the mix sample-exactly |
| package | local | `<title> [<bpm>bpm <key>].dawproject` — one track per stem, warp points 1:1 so nothing is stretched at project tempo |

Measured on the first track (10:29): GPU compute ≈ 4 min, ≈ $0.05; whole run incl. rental/setup/transfer ≈ 15–25 min, ≈ $0.20.
Vocals/instrumental residual: RoFormer chain −40.5 dBFS vs `htdemucs_6s` −30.6 dBFS.

## Install

```bash
cd tools/stemkit
python3 -m venv .venv && .venv/bin/pip install -e ".[test]"
vastai set api-key "$VAST_API_KEY"      # or export VAST_API_KEY; key lives in Infisical external/prod/ai/gpu-providers
```

Needs `ffmpeg`, `rsync`, `ssh` on PATH and an SSH key registered in the vast.ai account.

## Use

```bash
stemkit offers                                          # sanity-check the offer query and price
stemkit run "https://youtu.be/..." --out ~/Music/Stems  # full pipeline
stemkit run song.wav --out ./out --bpm 128 --key Am     # skip detection
stemkit run song.wav --out ./out --local-stems ./stems  # no GPU: package existing vocals/drums/bass/guitar/other.wav
stemkit analyze song.flac                               # BPM/key JSON only
stemkit dawproject --stems ./stems --bpm 137 --key C#m --title Song --out Song.dawproject
```

Output:

```
<out>/<title>/
  source/      original download + mix.flac (separation input)
  stems/       <title> - {vocals,drums,bass,guitar,other} [<bpm>bpm <key>].wav   (32-bit float; drums may exceed 0 dBFS)
  bitwig/      <title> [<bpm>bpm <key>].dawproject   (File > Open in Bitwig; Studio One and Cubase 14+ import it too)
  analysis.json
```

## Gotchas (all hit for real)

- The GPU instance is destroyed in a `finally`; `--keep-instance` opts out and prints the id. A forgotten 4090 is ~$10/day.
- Never destroy other instances in the account (`vastai show instances`): `meeting-transcription-lane1-recovery` is scribe's always-on box.
- `mix − vocals` can peak above 0 dBFS; the script halves it before the 6-stem model and doubles the outputs. Writing it as PCM_24 clips silently.
- MSST writes `.wav` instead of `.flac` when a stem peaks ≥ 1.0 — the assembler globs both.
- MSST `requirements.txt` does not install cleanly (wxpython/pyaudio/diffq); the script installs the inference subset only.
- The original `jarredou/BS-ROFO-SW-Fixed` HF repo is gone; the `enerjazzer` mirror is byte-identical (size is asserted).
- BPM/key are estimates. Key of a modulating song is a coin flip between relatives — `analysis.json` keeps the runner-up and per-section keys. BPM has the usual octave ambiguity (Master of Puppets came out as 105.9, i.e. half of 212) — pass `--bpm` when you know better.
- Analysis runs ~3 min of CPU for a 10-min track (harmonic separation per section) before the GPU is rented; the remote `pip install` is another ~2–9 min. A pre-baked image would remove the latter.

## Verified runs

| Track | Length | End-to-end | GPU rate | Result |
|---|---|---|---|---|
| The Fifth Extinction (manual prototype) | 10:29 | ~25 min | $0.43/h | sum == mix, opened in Bitwig 6.1.1 at 137 BPM |
| Pantera – Master of Puppets (`stemkit run`) | 8:33 | 663 s | $0.40/h | sum == mix (max err 0.0), tempo 106 in project.xml |
- Neither DAWproject nor Bitwig has a project-level key; it lives in metadata Comment and clip names.
