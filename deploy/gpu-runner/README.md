# GPU runner (`rk gpu-agent`)

The runner is a pull client: it never needs an inbound port. It leases a
job from `rk serve`, downloads the converted source through a signed URL,
runs Demucs on the GPU, uploads 24-bit/48 kHz stems through signed PUT
URLs, heartbeats progress, and completes or fails the lease.

## Image

```bash
# from the repository root
docker build -f deploy/gpu-runner/Dockerfile -t ghcr.io/kossoy/rk-gpu-runner:latest .
docker push ghcr.io/kossoy/rk-gpu-runner:latest
```

Base: `pytorch/pytorch:2.2.0-cuda12.1-cudnn8-runtime` + ffmpeg + `demucs==4.0.1`
with `htdemucs`, `htdemucs_ft` and `htdemucs_6s` pre-downloaded into
`/models` (`TORCH_HOME`). The `rk` binary is built in a `golang:1.26` stage.

## Configuration

| Variable / flag | Meaning |
|---|---|
| `RK_API_URL` / `--api` | base URL of `rk serve` (must be reachable from the GPU box) |
| `RK_RUNNER_TOKEN` / `--token` | shared secret, same value as on the server |
| `RK_RUNNER_ID` / `--id` | label in leases/logs (default: hostname) |
| `RK_DEMUCS_DEVICE` / `--device` | `cuda` (default) or `cpu` |
| `RK_DEMUCS_ARGS` / `--demucs-args` | extra demucs flags, e.g. `--segment 7` for GPUs with < 8 GB |
| `RK_WORK_DIR` / `--work-dir` | scratch (default `/work` in the image) |
| `RK_POLL_INTERVAL` / `--poll` | idle poll (default `5s`) |
| `RK_ONCE=1` / `--once` | process one job, then exit |

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
# on the instance: RK_API_URL=http://127.0.0.1:18080
```

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
* Three failed or expired leases fail the job.
