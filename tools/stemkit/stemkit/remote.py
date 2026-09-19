"""Move files to/from the GPU box and run the separation script there."""
from __future__ import annotations

import subprocess
import sys
from importlib import resources
from pathlib import Path

from .vast import Instance

REMOTE_IN = "/workspace/in/mix.flac"
REMOTE_OUT = "/workspace/stems"


def _rsync(inst: Instance, src: str, dst: str) -> None:
    ssh = " ".join(inst.ssh_base()[:-1])  # everything but the target; rsync supplies user@host itself
    subprocess.run(["rsync", "-rq", "--partial", "-e", ssh, src, dst], check=True)


def upload(inst: Instance, local_flac: Path) -> None:
    subprocess.run(inst.ssh_base() + ["mkdir", "-p", str(Path(REMOTE_IN).parent)], check=True)
    _rsync(inst, str(local_flac), f"{inst.ssh_target}:{REMOTE_IN}")


def run_separation(inst: Instance, log=sys.stderr) -> None:
    script = resources.files("stemkit").joinpath("remote/separate.sh").read_text()
    subprocess.run(inst.ssh_base() + ["cat > /workspace/separate.sh && chmod +x /workspace/separate.sh"],
                   input=script, text=True, check=True)
    proc = subprocess.Popen(inst.ssh_base() + [f"bash /workspace/separate.sh {REMOTE_IN} {REMOTE_OUT}"],
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    for line in proc.stdout:
        print(line.rstrip(), file=log)
    if proc.wait() != 0:
        raise RuntimeError("remote separation failed")


def download(inst: Instance, local_dir: Path) -> None:
    local_dir.mkdir(parents=True, exist_ok=True)
    _rsync(inst, f"{inst.ssh_target}:{REMOTE_OUT}/", str(local_dir) + "/")
