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
| `RK_SIGNED_URL_BASE` / `--signed-url-base` | replace `scheme://host` of the signed source/upload URLs, e.g. `http://127.0.0.1:18080` behind an ssh tunnel (the server builds them from `RK_PUBLIC_URL`, which the box may not reach; the signature covers only method, path and expiry) |
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
# on the instance: RK_API_URL=http://127.0.0.1:18080 RK_SIGNED_URL_BASE=http://127.0.0.1:18080
```

`RK_SIGNED_URL_BASE` matters whenever the server has `RK_PUBLIC_URL` set to
an address the box cannot reach: without it the agent follows the signed
URLs to the public host (for a LAN-only origin behind Cloudflare that is a
502) and the job burns its attempts.

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
