"""python3 -m unittest deploy/gpu-runner/test_fetch_models.py — pinning and verification of runner weights."""
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "fetch_models.py")
REPO = os.path.dirname(os.path.dirname(HERE))


class FetchModels(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        t = self.tmp.name
        self.src = os.path.join(t, "src")
        self.dest = os.path.join(t, "models")
        os.makedirs(self.src)
        files = {"pub.ckpt": b"public weights", "pub.yaml": b"cfg", "int.ckpt": b"internal weights"}
        cps = []
        for name, body in files.items():
            with open(os.path.join(self.src, name), "wb") as f:
                f.write(body)
            cps.append({"id": name, "class": "internal" if name.startswith("int") else "public", "licence": "x",
                        "path": "m/" + name, "size": len(body), "sha256": hashlib.sha256(body).hexdigest(),
                        "url": "file://" + os.path.join(self.src, name)})
        self.manifest = {"checkpoints": cps,
                         "recipes": [{"id": "p", "engine": "msst", "stems": 4, "checkpoints": ["pub.ckpt", "pub.yaml"]},
                                     {"id": "i", "engine": "msst", "stems": 6, "checkpoints": ["pub.ckpt", "int.ckpt"]}],
                         "stacks": {"public": {"high": "p"}, "internal": {"hifi": "i"}}}
        self.write_manifest()

    def tearDown(self):
        self.tmp.cleanup()

    def write_manifest(self):
        self.mpath = os.path.join(self.tmp.name, "manifest.json")
        with open(self.mpath, "w") as f:
            json.dump(self.manifest, f)

    def run_fetch(self, stack, *extra):
        return subprocess.run([sys.executable, SCRIPT, "--manifest", self.mpath, "--dest", self.dest, "--stack", stack, *extra],
                              capture_output=True, text=True)

    def test_public_gets_only_its_recipes(self):
        self.assertEqual(self.run_fetch("public").returncode, 0)
        self.assertEqual(sorted(os.listdir(os.path.join(self.dest, "m"))), ["pub.ckpt", "pub.yaml"])
        r = self.run_fetch("public", "--verify")
        self.assertEqual(r.returncode, 0, r.stderr)

    def test_bad_sha256_fails_the_build(self):
        self.manifest["checkpoints"][0]["sha256"] = "0" * 64
        self.write_manifest()
        r = self.run_fetch("public")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("checksum mismatch", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(self.dest, "m", "pub.ckpt")))

    def test_extra_file_fails_verify(self):
        self.run_fetch("public")
        with open(os.path.join(self.dest, "m", "int.ckpt"), "wb") as f:
            f.write(b"internal weights")
        r = self.run_fetch("public", "--verify")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("unexpected file", r.stderr)

    def test_public_stack_on_internal_weights_refused(self):
        self.manifest["stacks"]["public"]["hifi"] = "i"
        self.write_manifest()
        r = self.run_fetch("public")
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("non-public", r.stderr)

    def test_internal_gets_everything(self):
        self.assertEqual(self.run_fetch("internal").returncode, 0)
        self.assertEqual(len(os.listdir(os.path.join(self.dest, "m"))), 3)
        self.assertEqual(self.run_fetch("internal", "--verify").returncode, 0)

    def test_repo_manifest_public_selection(self):
        # The shipped manifest: the public image holds only public-class files.
        m = json.load(open(os.path.join(REPO, "internal", "models", "manifest.json")))
        sys.path.insert(0, HERE)
        import fetch_models
        chosen = fetch_models.select(m, "public")
        self.assertTrue(chosen)
        self.assertTrue(all(c["class"] == "public" for c in chosen))
        self.assertFalse(any(c["id"].startswith(("htdemucs", "bs_rofo", "adtof", "hfmidi")) for c in chosen))


if __name__ == "__main__":
    unittest.main()
