# GPU runner (`rk gpu-agent`)

The runner is a pull client: it never needs an inbound port. It leases a
job from `rk serve`, downloads the converted source through a signed URL,
runs the job's separation recipe on the GPU, uploads 24-bit/48 kHz stems through signed PUT
URLs, heartbeats progress, and completes or fails the lease.

## Images: one per model stack

Every job carries a model stack, chosen by the server from the account
(`RK_INTERNAL_EMAILS`, see `internal/models`):

* **public**: every account and anonymous upload. It runs SCNet XL IHF (Standard, 4 stems) and
  MelBand RoFormer Kim → SCNet XL IHF on the instrumental (Plus HiFi, 4 stems). Only
  public-class weights are allowed.
* **internal**: owner and allowlist. It runs everything above plus the Demucs presets, Kim →
  BS-RoFormer SW (6-stem HiFi) and transcription.

There are two build targets from one Dockerfile:

```bash
# from the repository root
docker build -f deploy/gpu-runner/Dockerfile --target public   -t ghcr.io/kossoy/rk-gpu-runner:public-<tag> .
docker build -f deploy/gpu-runner/Dockerfile --target internal -t ghcr.io/kossoy/rk-gpu-runner:internal-<tag> .
```

| | public | internal |
|---|---|---|
| Separation | MSST @ `84b1eac` (inference modules only, `requirements-separate.txt`) | same + `demucs==4.0.1` |
| Weights under `/models` | Kim, SCNet XL IHF + configs (1.1 GB) | all of `internal/models/manifest.json` |
| Transcription | none | Beat This!, ADTOF, hf_midi, optional MuScriptor |
| `RK_RUNNER_STACK` | `public` | `internal` (`RK_TRANSCRIBE=1`) |

The same checks run for both targets:

* **Pinning.** `fetch_models.py` downloads every checkpoint from the pinned URL in the manifest
  (revision-pinned HF path, release asset or content-addressed Demucs file). It checks size and
  sha256 and fails the build on a mismatch.
* **Build-time verify.** `fetch_models.py --verify`, the last build step, fails if a checkpoint is
  missing or changed, or if any other file sits under `/models`. On the public target it also
  fails if an internal-only package is installed (`demucs`, `adtof_pytorch`, `muscriptor`,
  `hf_midi_transcription`).
* **Runtime.** At startup the agent hashes every checkpoint again (`models.Scan`). It refuses to
  start if a public image holds any internal weights, and advertises only the recipes whose
  weights verified (`X-Runner-Recipes`, `X-Runner-Stack`). Before each lease it checks the recipe
  against the job's stack and its own stack (`models.Check`).
* **Egress lock.** Every Python child (separation, transcription) runs with `RK_EGRESS_DIR` on
  `PYTHONPATH`. Its `sitecustomize` blocks all non-loopback sockets and DNS, so no library can
  download weights or send anything during inference. Vast containers have no NET_ADMIN, so
  iptables is not an option there. The Go agent keeps its network for the API.

Recipe timings (A/B, [`docs/rebuild/ab-public-separator.md`](../../docs/rebuild/ab-public-separator.md)),
5:56 track on an RTX 3060: SCNet XL IHF 85 s, Kim + SCNet XL IHF 122 s.

### Transcription stack (internal image only)

The internal image carries the beat grid + MIDI adapters (`tools/transcribe/`, copied to
`/opt/rk/tools/transcribe`) and runs the agent with `RK_TRANSCRIBE=1`, so it advertises
`X-Runner-Features: transcribe`. The server offers transcription only to internal accounts.
Pins live in `requirements-transcribe.txt` (torch 2.2 / numpy<2); the weights are in the
manifest.

| Adapter | Package | Weights | Licence of the weights |
|---|---|---|---|
| grid: `beatthis` | `beat-this==1.1.0` (needs `rotary-embedding-torch<0.9`) | `/models/beat_this-final0.ckpt` (78 MB, cloud.cp.jku.at, sha256 in the manifest) | MIT (training data partly copyrighted) |
| drums: `adtof` | `xavriley/ADTOF-pytorch@85c192e` (5 classes → GM 36/38/42/48/49) | in the package (3.6 MB, sha256 checked by `--verify`) | **CC BY-NC-SA 4.0**. The package code is MIT; the ADTOF weights are not |
| guitar/bass/piano: `hfmidi` (default) | `xavriley/hf_midi_transcription@96f6797` | `/models/hf_midi/{guitar-gaps,filobass_20000_iterations,piano}.pth` (300 MB, HF `xavriley/midi-transcription-models` @ `b7bec65a`) | `piano.pth`: **CC BY 4.0, Edwards et al.** (Zenodo 10610212, attribution required). guitar/bass: MIT tag on the HF repo, but GAPS and FiloBass data are non-commercial |
| guitar/bass/piano: `muscriptor` | `muscriptor==0.3.0` (later releases need torch ≥ 2.3) | **not downloaded by the build**: the weights are CC BY-NC 4.0 and gated on HF. Accept the licence, then on the build host run `hf download MuScriptor/muscriptor-medium model.safetensors config.json --local-dir deploy/gpu-runner/models/muscriptor/medium` (gitignored) and rebuild. The token never enters the image | code MIT, weights CC BY-NC |
| sections | none in S1 (`off`) | — | — |

Adapter selection: `RK_ADAPTER_GRID`, `RK_ADAPTER_DRUMS`, `RK_ADAPTER_BASS`,
`RK_ADAPTER_GUITAR`, `RK_ADAPTER_PIANO`, `RK_ADAPTER_SECTIONS` (`off` skips;
`muscriptor` needs the baked weights, `RK_MUSCRIPTOR_SIZE=medium`,
`RK_MUSCRIPTOR_DTYPE=bfloat16` on Ampere+). Each adapter runs under its own
timeout (`RK_TRANSCRIBE_TIMEOUT_GRID` 5m, `RK_TRANSCRIBE_TIMEOUT_NOTES` 15m per
stem); a failure is recorded in `analysis.json` (`"status": "failed"`, reason)
and the job still completes with its stems — the runner never calls `/fail`
for an adapter. `HF_HUB_OFFLINE=1` at runtime: nothing is fetched on a lease.

Adapter CLI contract (also how to run one by hand inside the image):

```bash
python /opt/rk/tools/transcribe/grid_beatthis.py --input mix.wav --output grid.json --device cuda
python /opt/rk/tools/transcribe/notes_hfmidi.py --input guitar.wav --output notes.json --stem guitar --device cuda
python /opt/rk/tools/transcribe/notes_adtof.py  --input drums.wav  --output notes.json --stem drums  --device cuda
```

`grid.json` = `{"beats": [s], "downbeats": [s], "source", "model"}`; `notes.json` =
`{"stem", "adapter", "model", "notes": [{"onset", "offset", "pitch", "velocity" 0..1}]}`
(seconds, MIDI keys; GM drum notes for drums). Image size and pull time are
recorded in the PR that introduced the tag.

## Configuration

| Variable / flag | Meaning |
|---|---|
| `RK_API_URL` / `--api` | base URL of `rk serve` (must be reachable from the GPU box) |
| `RK_RUNNER_TOKEN` / `--token` | shared secret, same value as on the server |
| `RK_RUNNER_ID` / `--id` | label in leases/logs (default: hostname) |
| `RK_DEMUCS_DEVICE` / `--device` | `cuda` (default) or `cpu` |
| `RK_DEMUCS_ARGS` / `--demucs-args` | extra demucs flags, e.g. `--segment 7` for GPUs with < 8 GB |
| `RK_REBASE_SIGNED_URLS=1` / `--rebase-urls` | rebase the signed source/upload URLs of a lease onto `RK_API_URL` (the server builds them from `RK_PUBLIC_URL`, which the box may not reach; the signature covers only method, path and expiry). **Default on when `RK_API_URL` is a loopback address**, i.e. behind an ssh tunnel |
| `RK_SIGNED_URL_BASE` / `--signed-url-base` | rebase onto this `scheme://host[:port]` instead of `RK_API_URL` |
| `RK_WORK_DIR` / `--work-dir` | scratch (default `/work` in the image) |
| `RK_POLL_INTERVAL` / `--poll` | idle poll (default `5s`) |
| `RK_ONCE=1` / `--once` | process one job, then exit |
| `RK_RUNNER_STACK` / `--stack` | `public` or `internal` (set by the image). Empty is a pre-registry runner: Demucs only, no recipe list |
| `RK_MODELS_DIR` / `--models-dir` | pinned weights, verified at startup (`/models`) |
| `RK_MODELS_MANIFEST` / `--models-manifest` | manifest for the separation script (`/opt/rk/models.json`) |
| `RK_SEPARATE_TOOL` / `--separate-tool` | MSST recipe runner (`/opt/rk/tools/separate/separate.py`) |
| `RK_EGRESS_DIR` / `--egress-dir` | egress lock for Python children (`/opt/rk/tools/egress`) |
| `RK_TRANSCRIBE=1` / `--transcribe` | advertise and run the transcription adapters (internal image; refused on a public runner) |
| `RK_TRANSCRIBE_TOOLS` / `--transcribe-tools` | adapter scripts directory (`/opt/rk/tools/transcribe` in the image) |
| `RK_TRANSCRIBE_DEVICE` / `--transcribe-device` | adapters' device (default `--device`) |
| `RK_ADAPTER_{GRID,DRUMS,BASS,GUITAR,PIANO,SECTIONS}` | adapter per stem: `beatthis`, `adtof`, `hfmidi`, `muscriptor`, `off` |
| `RK_TRANSCRIBE_TIMEOUT_GRID` / `RK_TRANSCRIBE_TIMEOUT_NOTES` | per-adapter timeouts (5m / 15m) |

## vast.ai flow

```bash
# 1. pick an offer: 12 GB+ VRAM, one GPU, reliable, fast link
vastai search offers 'gpu_ram>=12 num_gpus=1 dph<0.35 reliability>0.95 inet_down>200 disk_space>=30 cuda_vers>=12.1' -o dph+

# 2. rent it with the image (ghcr.io is private → pass a login)
vastai create instance <OFFER_ID> \
  --image ghcr.io/kossoy/rk-gpu-runner:latest \
  --login '-u kossoy -p <ghcr token> ghcr.io' \
  --disk 30 --ssh --direct \
  --env '-e RK_API_URL=https://rk.example.com -e RK_RUNNER_TOKEN=... -e RK_RUNNER_ID=vast-1' \
  --onstart-cmd 'nohup rk gpu-agent > /var/log/rk-gpu-agent.log 2>&1 &'

# 3. watch
vastai show instances
vastai ssh-url <INSTANCE_ID>       # ssh in, tail /var/log/rk-gpu-agent.log

# 4. destroy when done (billing stops)
vastai destroy instance <INSTANCE_ID>
vastai show instances               # confirm it is gone
```

If `rk serve` is not publicly reachable (development on a LAN), tunnel it
into the instance instead of exposing it:

```bash
ssh -N -R 18080:127.0.0.1:18080 -p <PORT> root@<sshN.vast.ai>
# on the instance: RK_API_URL=http://127.0.0.1:18080   (loopback → signed URLs are rebased onto it)
```

The rebase matters whenever the server's `RK_PUBLIC_URL` is an address the
box cannot reach: without it the agent follows the signed URLs to the
public host (for a LAN-only origin behind Cloudflare that is a 502) and the
job burns its attempts. With the rebase in the agent, `RK_PUBLIC_URL` on
the server can stay the real hostname; setting it to the tunnel address
instead (`http://127.0.0.1:18080`) also works but ties the server config
to one runner topology.

## Automatic: `rk gpu-scaler`

The manual flow above is what `rk gpu-scaler` does on its own, on a host
with outbound internet, `vastai` and ssh (not on the server): it polls
`GET /api/v1/gpu/queue`, rents the cheapest matching offer when a job
waits, opens the reverse ssh tunnel into the instance, starts the agent
through the on-start script, and destroys the instance after 10 minutes
with nothing waiting and no active lease (also at 6 h of age, or when the
box never starts). One instance at most; it never rents below $5 of
credit and only ever destroys instances carrying its own label.

```bash
# on the vast.ai-facing host
go build -o ~/.local/bin/rk ./cmd/rk
install -Dm600 scripts/gpu/scaler.env.example ~/.config/rk/scaler.env   # RK_API_URL, RK_RUNNER_TOKEN, …
install -Dm644 deploy/gpu-runner/rk-gpu-scaler.service ~/.config/systemd/user/
loginctl enable-linger "$USER"
systemctl --user daemon-reload && systemctl --user enable --now rk-gpu-scaler
journalctl --user -u rk-gpu-scaler -f     # rent → running → tunnel verified → first lease → destroyed
rk-gpu-scaler status                      # state file: instance, timestamps, cost history
```

What it passes to `vastai create instance`: `--image $RK_SCALER_IMAGE
--login <from ~/.docker/config.json> --ssh --direct --disk 30 --label
rk-gpu-scaler --cancel-unavail`, `--env '-e RK_API_URL=http://127.0.0.1:18080
-e RK_RUNNER_TOKEN=…'` and an `--onstart-cmd` that waits for `/healthz`
through the tunnel and then runs `rk gpu-agent` (log:
`/var/log/rk-gpu-agent.log` on the instance). Policy and variables:
[`docs/rebuild/README.md`](../../docs/rebuild/README.md#gpu-autoscaler-rk-gpu-scaler),
`scripts/gpu/scaler.env.example`.

## Runtime notes

* Demucs writes 24-bit FLAC (`--flac --int24`) at 44.1 kHz; ffmpeg then
  resamples to 24-bit/48 kHz stereo WAV, which is what the server verifies.
* Progress comes from the tqdm bars on stderr after the `Separating track`
  line; `htdemucs_ft` is a bag of four models and shows four bars, which the
  agent folds into one 0..1 value. A model-download bar (only on images
  without pre-downloaded models) is ignored.
* Runner-side timings on an RTX 3060 (vast.ai, Sept 2026): 629 s of audio
  through `htdemucs_6s` in 29 s of demucs wall time; the whole lease
  (download 181 MB source, separate, convert, upload six 181 MB stems
  through an ssh tunnel) took 97 s. A 3-minute track through
  `htdemucs_ft` took 45 s lease-to-complete; 1 minute through `htdemucs`
  11 s.
* A lease expires after `RK_GPU_LEASE_TTL` (server side, default 10 min)
  without a heartbeat; the agent heartbeats every `TTL/4`. If the server
  answers 409/410 the job was cancelled and demucs is killed.
* After a failed job the agent waits `RK_POLL_INTERVAL` before leasing
  again, so a broken runner environment cannot use up a job's three
  attempts within seconds.
* Three failed or expired leases fail the job.
* `GET /api/v1/gpu/queue` (runner token) answers `{"waiting": N,
  "active_leases": M, "oldest_waiting_at": …}` for autoscalers; `waiting`
  is what the next lease call would be offered.
