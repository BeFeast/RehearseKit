"""Egress lock for inference subprocesses on the GPU runner.

Python imports sitecustomize at start-up when this directory is on PYTHONPATH. With
RK_EGRESS_LOCK=1 every outbound socket connection that is not loopback or a Unix socket
raises, and DNS lookups of non-local names fail, so no library (torch.hub, huggingface_hub,
audio-separator, demucs remote) can fetch weights or send anything while a job is being
separated or transcribed. The runner's own Go process keeps its network: it talks to the
API and uploads the stems, the Python children never need to.

Vast containers run without NET_ADMIN, so an iptables OUTPUT policy is not available there;
this is the lock that works on every host.
"""
import os
import socket

if os.environ.get("RK_EGRESS_LOCK") == "1":
    _LOCAL_NAMES = {"localhost", "localhost.localdomain", "ip6-localhost", "ip6-loopback"}

    class EgressBlocked(OSError):
        pass

    def _local(host):
        if host in _LOCAL_NAMES:
            return True
        try:
            import ipaddress

            return ipaddress.ip_address(host.split("%")[0]).is_loopback
        except ValueError:
            return False

    def _check(address, family):
        if family == getattr(socket, "AF_UNIX", None):
            return
        host = address[0] if isinstance(address, tuple) else str(address)
        if not _local(str(host)):
            raise EgressBlocked(f"egress blocked by RK_EGRESS_LOCK: {address!r}")

    _connect, _connect_ex = socket.socket.connect, socket.socket.connect_ex
    _sendto = socket.socket.sendto

    def connect(self, address):
        _check(address, self.family)
        return _connect(self, address)

    def connect_ex(self, address):
        _check(address, self.family)
        return _connect_ex(self, address)

    def sendto(self, data, *args):
        _check(args[-1], self.family)
        return _sendto(self, data, *args)

    socket.socket.connect = connect
    socket.socket.connect_ex = connect_ex
    socket.socket.sendto = sendto

    _getaddrinfo = socket.getaddrinfo

    def getaddrinfo(host, *args, **kwargs):
        if host is not None and not _local(host if isinstance(host, str) else host.decode()):
            raise socket.gaierror(socket.EAI_NONAME, f"egress blocked by RK_EGRESS_LOCK: {host}")
        return _getaddrinfo(host, *args, **kwargs)

    socket.getaddrinfo = getaddrinfo

    # Belt and braces for libraries that honour these before touching the network.
    for k, v in {"HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "HF_DATASETS_OFFLINE": "1"}.items():
        os.environ[k] = v
