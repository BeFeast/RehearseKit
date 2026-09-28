# Drum preview kit (`web/public/kit/`)

The DRUM EDITOR's **MIDI KIT** audition plays the edited hits through a
small sample kit in the browser while the mixer streams the other stems.
It is a preview: the real drum sound comes from the DAW (Bitwig, SD3, …)
through the exported `drums.mid` / `drums.dawproject`.

## Source and licence

- Samples: [Virtuosity Drums](https://github.com/sfzinstruments/virtuosity_drums)
  by Versilian Studios and Karoryfer Samples, **CC0 1.0** (public domain
  dedication; the `LICENSE` file of the repository at commit
  `9f04cf9a734527edfbb0a4eee1f674e45bbf71bc`). No attribution is required; this file records the
  origin so the kit can be traced and rebuilt.
- Mic position used: overhead pair (`Samples/oh`, stereo) for everything except the kick, which comes from the close mic (`Samples/kickmic`, mono) — the overhead kick is mostly room (−20 dBFS).
- Substitutions (articulations the kit has no dedicated sample for):
  `tomm` = the high tom pitched down 3 semitones (the kit has two toms; measured fundamentals 183 → 153 → 113 Hz for high/mid/floor), `tomf` = the kit's low tom, `stick` = the snare cross-stick. Ride bell has its own samples. Velocity layers: three per articulation (SFZ layers vl2/3/4 or the 3-layer cymbals as is; 16- and 36-layer drums sampled at soft/medium/hard); the ghost layer of kick/hats folded into layer 1. Samples are 16-bit FLAC (24-bit would be ~7 MB), one gain per articulation with the hardest layer at −1 dBFS.

## Files

```
web/public/kit/kit.json        manifest: articulation → velocity layers (file, vel range, gain)
web/public/kit/<art>-<n>.flac  48 kHz FLAC, trimmed and faded, n = 1 (soft) … 3 (hard)
web/scripts/build-kit.sh       re-runnable: downloads the chosen source WAVs by URL and rebuilds out/ with ffmpeg
```

`kit.json` keys are the editor's articulations (`kick`, `snare`, `stick`,
`hhc`, `hho`, `hhp`, `tomh`, `tomm`, `tomf`, `ride`, `bell`, `crash`); each
layer covers a 1..127 velocity range and the ranges of one articulation
tile 1..127 without gaps. Total size is kept under 4 MB (3,120,535 bytes for 36 samples + manifest).

## Playback (`web/src/player/kit/`)

- `kit.ts` — fetches the manifest, decodes every layer with the
  **engine's** `AudioContext` (`decodeAudioData`), picks the layer for a
  velocity and shades the gain inside the layer (0.55 … 1.0 × layer gain).
- `scheduler.ts` — `KitScheduler` queues hits ahead of the audio clock.
  The engine streams a linear sequence of frames (a prefix, then the loop
  region repeated); the AudioWorklet publishes the pair (stream frame
  `READ_POS`, audio frame `CLOCK_FRAME`) every quantum, so a hit at song
  second *t* maps to one stream frame per pass and is queued exactly once
  per pass — a loop wrap cannot double-trigger it, and nothing is timed
  from the UI clock. Every seek/replan bumps the engine's stream
  generation: queued voices are cancelled and the cursor restarts at the
  clock; edits, row mutes and mode changes cancel what has not sounded yet
  and rescan; an underrun (the stream stalled while the audio clock ran)
  does the same. A closed or pedal hat chokes the open hat still ringing.
  Lookahead 120 ms, tick 25 ms, hits more than 30 ms in the past are
  skipped.
- `use-kit-audition.ts` — while the editor is in MIDI KIT mode the drum
  stem is force-muted in the engine (a layer over the mixer's mute, mix
  state untouched); the kit bus follows the DRUMS strip (fader, mute,
  solo) and joins the master after the stem faders (`StreamEngine.auxInput`).
  The inspector's AUDITION plays the selected hits (≤ 12) immediately,
  keeping their relative timing.

ORIGINAL mode plays the drum stem as the mixer has it; SPLIT (per-instrument
sub-stems) is not part of this slice.
