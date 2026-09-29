# Credits and model licences

Which model weights run where, under which licence, and the attributions the service must show.
The pinned files (source revision, size, sha256) are in `internal/models/manifest.json`. The
licence research behind the classes is the 2026-09-28 audit (#35).

## Public stack (every account and anonymous upload)

| Role | Model | Weights licence | Code | Attribution |
|---|---|---|---|---|
| 4 stems (Standard), and drums/bass/other in Plus HiFi | SCNet XL IHF, MSST release v1.0.15 (`model_scnet_ep_36_sdr_10.0891.ckpt`) | MIT (author's statement, ZFTurbo/Music-Source-Separation-Training #245) | MSST, MIT | Roman Solovyev (ZFTurbo), SCNet by Tong et al. |
| Vocals in Plus HiFi | MelBand RoFormer, Kimberley Jensen edition (HF `KimberleyJSN/melbandroformer` @ `ac9b0614`) | MIT (HF licence tag; author in KimberleyJensen/Mel-Band-Roformer-Vocal-Model #18) | MSST, MIT | Kimberley Jensen; MelBand RoFormer by Wang et al.; UVR community |
| BPM, key | librosa 0.11 | no weights | ISC | librosa development team |

Training-data caveat, recorded here so it is not lost: both separators were trained on data that
includes MUSDB18-HQ (non-commercial) and, for Kim, MoisesDB (CC BY-NC-SA) and private tracks. The
publishers licence the weights permissively; whether the dataset terms reach the weights is the
open legal question of the audit. A lawyer signs off before the paid launch.

Model code in the runner image: Music-Source-Separation-Training at commit `84b1eac` (MIT), only
the inference modules. Not installed in the public image: `demucs`, `diffq` (CC BY-NC),
`pedalboard` (GPL), `audio-separator`.

## Internal stack only (owner and allowlist, `RK_INTERNAL_EMAILS`; never sold)

These run only on jobs of internal accounts and ship only in the internal runner image. A public
job cannot select them: the server, the database constraint, the runner and the image build each
refuse.

| Role | Model | Weights licence |
|---|---|---|
| Fast / Standard / 6-stem presets | Demucs `htdemucs`, `htdemucs_ft`, `htdemucs_6s` (Meta) | Research-only by the author's statements (facebookresearch/demucs #327, #384, #508); code MIT |
| 6-stem HiFi (guitar, piano) | BS-RoFormer SW (`BS-Rofo-SW-Fixed.ckpt`, HF `enerjazzer/BS-ROFO-SW-Fixed` @ `a443a298`) | Unknown: no rights holder has licensed it |
| Beat grid | Beat This! `final0` (CPJKU) | MIT (training data partly copyrighted) |
| Drum notes | ADTOF (ADTOF-pytorch @ `85c192e`) | CC BY-NC-SA 4.0 (not MIT) |
| Guitar / bass notes | hf_midi `guitar-gaps.pth`, `filobass_20000_iterations.pth` (xavriley) | MIT tag on the HF repo; GAPS and FiloBass data are non-commercial |
| Piano notes | hf_midi `piano.pth` | CC BY 4.0: Edwards et al., Zenodo 10610212 |
| Notes, alternative | MuScriptor (optional, gated) | CC BY-NC 4.0 |

## Other material

- Drum kit samples in the SPA (`web/public/kit`): CC0 1.0, sfzinstruments/virtuosity_drums
  (`docs/rebuild/drum-kit.md`).
