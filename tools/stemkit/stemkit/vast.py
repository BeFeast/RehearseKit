"""Thin wrapper over the `vastai` CLI: pick an offer, rent it, wait for SSH, destroy it.

Auth: `vastai set api-key ...` (stored in ~/.config/vastai/vast_api_key) or VAST_API_KEY env.
Instances are ALWAYS destroyed by the caller (try/finally) — a forgotten 4090 costs ~$10/day.
"""
from __future__ import annotations

import json
import subprocess
import time
from dataclasses import dataclass

DEFAULT_IMAGE = "pytorch/pytorch:2.5.1-cuda12.4-cudnn9-runtime"
DEFAULT_QUERY = ("gpu_name=RTX_4090 num_gpus=1 cuda_vers>=12.1 disk_space>=60 reliability>0.98 "
                 "inet_down>500 inet_up>200 direct_port_count>=1 rentable=true verified=true")


def _vast(*args: str, raw: bool = True) -> object:
    cmd = ["vastai", *args] + (["--raw"] if raw else [])
    p = subprocess.run(cmd, capture_output=True, text=True)
    if p.returncode != 0:
        raise RuntimeError(f"vastai {' '.join(args)} failed: {p.stderr.strip() or p.stdout.strip()}")
    if not raw:
        return p.stdout
    # the CLI occasionally prints a warning line before the JSON
    txt = p.stdout[p.stdout.find("[") if p.stdout.lstrip().startswith("[") else p.stdout.find("{"):]
    return json.loads(txt)


@dataclass
class Offer:
    id: int
    dph: float
    gpu: str
    geo: str
    inet_down: float
    reliability: float


@dataclass
class Instance:
    id: int
    host: str = ""
    port: int = 0
    dph: float = 0.0

    @property
    def ssh_target(self) -> str:
        return f"root@{self.host}"

    def ssh_base(self) -> list[str]:
        return ["ssh", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
                "-o", "LogLevel=ERROR", "-o", "ConnectTimeout=20", "-p", str(self.port), self.ssh_target]


def search_offers(query: str = DEFAULT_QUERY, limit: int = 10) -> list[Offer]:
    rows = _vast("search", "offers", query, "-o", "dph")
    return [Offer(r["id"], r["dph_total"], r["gpu_name"], r.get("geolocation", ""), r.get("inet_down", 0.0),
                  r.get("reliability2", 0.0)) for r in rows[:limit]]


def create_instance(offer_id: int, image: str = DEFAULT_IMAGE, disk_gb: int = 60, label: str = "stemkit") -> Instance:
    r = _vast("create", "instance", str(offer_id), "--image", image, "--disk", str(disk_gb), "--ssh", "--direct",
              "--label", label, "--onstart-cmd", "touch ~/.no_auto_tmux")
    if not r.get("success"):
        raise RuntimeError(f"create instance failed: {r}")
    return Instance(id=int(r["new_contract"]))


def wait_running(inst: Instance, timeout: int = 600) -> Instance:
    """Poll until the instance reports `running` with a direct SSH port, then until sshd answers."""
    t0 = time.time()
    while time.time() - t0 < timeout:
        info = _vast("show", "instance", str(inst.id))
        port = (info.get("ports") or {}).get("22/tcp", [{}])[0].get("HostPort")
        if info.get("actual_status") == "running" and port:
            inst.host = info["public_ipaddr"]
            inst.port = int(port)
            inst.dph = float(info.get("dph_total") or 0.0)
            break
        time.sleep(10)
    else:
        raise TimeoutError(f"instance {inst.id} not running after {timeout}s")
    while time.time() - t0 < timeout:
        p = subprocess.run(inst.ssh_base() + ["true"], capture_output=True)
        if p.returncode == 0:
            return inst
        time.sleep(8)
    raise TimeoutError(f"sshd on instance {inst.id} not reachable after {timeout}s")


def destroy_instance(inst: Instance) -> None:
    _vast("destroy", "instance", str(inst.id), "-y", raw=False)


def credit() -> float:
    return float(_vast("show", "user").get("credit") or 0.0)
