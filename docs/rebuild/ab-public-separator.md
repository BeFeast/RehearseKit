# A/B: public 4-stem separator (#35)

Which separator the public stack uses: **SCNet XL IHF** (MSST v1.0.15) or **oBSRNN-SIMO**
(Zenodo 17516442). The current High preset, `htdemucs_ft`, is measured as the baseline.
Measured 2026-09-29 on rented Vast RTX 3060 and RTX 4090 machines. Total cost ≈ $0.30.

**Choice: SCNet XL IHF.** It is best on every drums/bass/other figure. It beats oBSRNN-SIMO by
+0.9 dB SI-SDR and the current `htdemucs_ft` by +1.1 dB, averaged over drums, bass and other
on both tracks. It fits the scaler's 12 GB GPUs. It is about half as fast as oBSRNN-SIMO on a
3060, but still 4× realtime.

## Material

Two real multitracks. Each mix is the exact sum of its stems, and each stem group is the
reference, so no alignment step is involved.

| Track | Source | Length | References |
|---|---|---|---|
| T1 | Commercial band multitrack: drums, bass, 2 guitars, 2 keys; no vocals | 5:56 | drums, bass, other = guitars + keys |
| T2 | Owner's own production with vocals | 1:22 | vocals = 2 vocal tracks, drums, bass, other = 3 pianos + chords |

Neither model was trained on either track; both were trained on MUSDB18-HQ only. The
material is small (two tracks), so the drums/bass/other means matter more than any single
cell. Vocals are only on T2.

## Quality (dB, higher is better)

SDR is the plain utterance SDR used by MSST and MVSEP. SI-SDR is scale-invariant.
`kim+X` = MelBand RoFormer (Kim) takes the vocals, and X separates mix − vocals. This is the
public Plus HiFi chain.

| Config | T1 drums | T1 bass | T1 other | T2 vocals | T2 drums | T2 bass | T2 other | **mean d/b/o SI-SDR** |
|---|---|---|---|---|---|---|---|---|
| SCNet XL IHF | 9.43 / 8.91 | 8.47 / 7.81 | 9.61 / 9.12 | 5.77 / 4.44 | 12.97 / 12.78 | 13.77 / 13.64 | 3.07 / 1.80 | **9.01** |
| Kim + SCNet XL IHF | 9.43 / 8.91 | 8.47 / 7.81 | 9.59 / 9.10 | 5.82 / 4.51 | 13.23 / 13.05 | 13.85 / 13.72 | 3.15 / 1.89 | **9.08** |
| oBSRNN-SIMO | 8.35 / 7.66 | 7.67 / 6.91 | 9.05 / 8.48 | 5.99 / 4.74 | 11.50 / 11.18 | 13.17 / 13.00 | 3.13 / 1.64 | 8.15 |
| Kim + oBSRNN-SIMO | 8.35 / 7.67 | 7.67 / 6.91 | 9.10 / 8.54 | 5.82 / 4.51 | 11.50 / 11.19 | 13.34 / 13.18 | 3.02 / 1.62 | 8.18 |
| `htdemucs_ft` (current, research-only weights) | 8.32 / 7.63 | 8.04 / 7.30 | 9.00 / 8.41 | 6.16 / 5.00 | 10.47 / 10.07 | 12.95 / 12.72 | 3.22 / 1.60 | 7.95 |

Cells are SDR / SI-SDR.

- SCNet XL IHF leads on drums on both tracks (+1.1 and +1.5 dB SDR over oBSRNN-SIMO) and on
  bass.
- The Kim chain adds a little on T2 drums and bass: SCNet works on an instrumental.
- On T1 (no vocals) Kim's near-silent vocal estimate changes nothing.
- Kim's vocals on T2 are 0.3 dB below `htdemucs_ft`. On one short track with a heavily
  processed vocal, that is noise, not a ranking. The published multisong figures (Kim 11.08
  vs `htdemucs_ft` 8.38) stand until more vocal material is measured.

## Speed and VRAM

Seconds for the 5:56 track, with ×realtime in brackets. VRAM is the peak allocated by torch;
the reserved peak is in brackets.

| Config | RTX 3060 12 GB | RTX 4090 24 GB | VRAM GB |
|---|---|---|---|
| SCNet XL IHF | 85 s (4.2×) | 21 s (17×) | 5.3 (9.7) |
| Kim + SCNet XL IHF | 122 s (2.9×) | 31 s (11×) | 6.1 (9.4) |
| oBSRNN-SIMO | 47 s (7.6×) | 18 s (20×) | 2.7 (7.3) |
| Kim + oBSRNN-SIMO | 82 s (4.4×) | 30 s (12×) | 3.6 (9.9–11.6) |
| `htdemucs_ft` | 46 s (7.8×) | 61 s (5.9×)* | 1.0 (1.3) |

\* `htdemucs_ft` runs four models and is CPU-bound on that host (256 threads, slow single
core). The 4090 figure is not a GPU figure.

Model load (warm disk): SCNet 1.6–2 s, Kim + SCNet 3.8–4.6 s, oBSRNN 7–8 s. All
configurations fit the scaler's `gpu_ram>=12` offers. SCNet's inference config
(`batch_size: 4`, `num_overlap: 4`) was used as shipped; AMP is on.

## Licence (from the 2026-09-28 audit)

Both candidates are in the "conditional" class: permissive weights, MUSDB18-HQ
(non-commercial) training data.

- oBSRNN-SIMO has the more formal licence record: CC BY 4.0 on Zenodo.
- SCNet XL IHF has MIT from the author in MSST issue #245. Before the paid launch, ask for a
  licence line that names this checkpoint.

The quality gap (+0.9 dB) decides the choice. The licence difference does not, because both
end up in the same class.

## Reproduce

`tools/sepbench/`: `prep.py` builds the mixes and references, `setup.sh` prepares a Vast box
(code pinned by commit, checkpoints by sha256 in `checkpoints.txt`), `run.py` separates and
times, `score.py` scores. Rent `pytorch/pytorch:2.7.0-cuda12.6-cudnn9-runtime` with a label
`rk-ab35-*`.
