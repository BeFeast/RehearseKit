"""python3 -m unittest tools/egress/test_egress.py — the lock blocks outbound, keeps loopback."""
import os
import socket
import subprocess
import sys
import threading
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def run(code, lock=True):
    env = dict(os.environ, PYTHONPATH=HERE, RK_EGRESS_LOCK="1" if lock else "0")
    return subprocess.run([sys.executable, "-c", code], env=env, capture_output=True, text=True, timeout=30)


class EgressLock(unittest.TestCase):
    def test_outbound_tcp_blocked(self):
        r = run("import socket; socket.create_connection(('1.1.1.1', 443), timeout=3)")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("egress blocked", r.stderr)

    def test_dns_blocked(self):
        r = run("import socket; socket.getaddrinfo('huggingface.co', 443)")
        self.assertIn("egress blocked", r.stderr)

    def test_urllib_download_blocked(self):
        r = run("import urllib.request; urllib.request.urlopen('https://dl.fbaipublicfiles.com/demucs/x.th', timeout=3)")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("egress blocked", r.stderr)

    def test_udp_sendto_blocked(self):
        r = run("import socket; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.sendto(b'x', ('8.8.8.8', 53))")
        self.assertIn("egress blocked", r.stderr)

    def test_offline_env(self):
        r = run("import os; print(os.environ['HF_HUB_OFFLINE'])")
        self.assertEqual(r.stdout.strip(), "1")

    def test_loopback_allowed(self):
        srv = socket.socket()
        srv.bind(("127.0.0.1", 0))
        srv.listen(1)
        port = srv.getsockname()[1]
        threading.Thread(target=lambda: srv.accept()[0].close(), daemon=True).start()
        r = run(f"import socket; socket.create_connection(('127.0.0.1', {port}), timeout=3).close(); "
                f"socket.create_connection(('localhost', {port}), timeout=3).close(); print('ok')")
        srv.close()
        self.assertEqual(r.stdout.strip(), "ok", r.stderr)

    def test_unlocked_is_untouched(self):
        r = run("import socket; print(socket.socket.connect.__module__ if hasattr(socket.socket.connect, '__module__') else 'builtin')", lock=False)
        self.assertNotIn("sitecustomize", r.stdout)


if __name__ == "__main__":
    unittest.main()
