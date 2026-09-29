#!/usr/bin/env python3
"""fetch_models.py — put exactly the manifest's weights for one runner stack into the image.

  fetch_models.py --manifest /opt/rk/models.json --dest /models --stack public            # download
  fetch_models.py --manifest /opt/rk/models.json --dest /models --stack public --verify   # final check

Download: every checkpoint the stack needs is fetched from its pinned URL (a revision-pinned
Hugging Face path, a release asset, a content-addressed Demucs file) and kept only if size
and sha256 match the manifest; any mismatch fails the build.

The public stack needs the checkpoints of the recipes stacks.public maps to. The internal
stack needs every checkpoint (its runner also serves public jobs, and it runs transcription).

--verify is the last build step: every selected checkpoint is present and matches, and no
other file sits under --dest (except --allow prefixes, e.g. the optional gated MuScriptor
weights on the internal image). On the public stack it also fails if any internal-class
checkpoint or an internal-only package (demucs, adtof_pytorch, muscriptor) is installed.
"""
import argparse, hashlib, importlib.util, json, os, sys, urllib.request

INTERNAL_ONLY_PACKAGES = ("demucs", "adtof_pytorch", "muscriptor", "hf_midi_transcription")


def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def select(manifest, stack):
    cps = {c["id"]: c for c in manifest["checkpoints"]}
    if stack == "internal":
        return list(cps.values())
    recipes = {r["id"]: r for r in manifest["recipes"]}
    ids = []
    for rid in manifest["stacks"]["public"].values():
        for cid in recipes[rid]["checkpoints"]:
            if cid not in ids:
                ids.append(cid)
    out = [cps[i] for i in ids]
    bad = [c["id"] for c in out if c["class"] != "public"]
    if bad:
        sys.exit(f"public stack references non-public checkpoints: {bad}")
    return out


def package_path(c):
    spec = importlib.util.find_spec(c["package"])
    if spec is None or not spec.submodule_search_locations:
        return None
    return os.path.join(list(spec.submodule_search_locations)[0], c["path"])


def check(c, path):
    if not os.path.isfile(path):
        return f"{c['id']}: missing {path}"
    if os.path.getsize(path) != c["size"]:
        return f"{c['id']}: {path} is {os.path.getsize(path)} bytes, manifest says {c['size']}"
    got = sha256(path)
    if got != c["sha256"]:
        return f"{c['id']}: {path} sha256 {got}, manifest says {c['sha256']}"
    return None


def download(c, dest):
    path = os.path.join(dest, c["path"])
    if os.path.isfile(path) and check(c, path) is None:
        print(f"ok (cached) {c['id']}")
        return
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".part"
    req = urllib.request.Request(c["url"], headers={"User-Agent": "rk-fetch-models"})
    with urllib.request.urlopen(req, timeout=600) as r, open(tmp, "wb") as f:
        while chunk := r.read(1 << 20):
            f.write(chunk)
    os.replace(tmp, path)
    err = check(c, path)
    if err:
        os.remove(path)
        sys.exit("checksum mismatch — " + err)
    print(f"ok {c['id']} {c['size']} B {c['sha256'][:12]}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", required=True)
    ap.add_argument("--dest", required=True)
    ap.add_argument("--stack", required=True, choices=["public", "internal"])
    ap.add_argument("--verify", action="store_true")
    ap.add_argument("--allow", action="append", default=[], help="extra path prefix allowed under --dest")
    a = ap.parse_args()
    manifest = json.load(open(a.manifest))
    chosen = select(manifest, a.stack)

    if not a.verify:
        for c in chosen:
            if c.get("package"):
                continue  # ships inside a pinned pip package; checked by --verify
            download(c, a.dest)
        return

    errors = []
    expected = set()
    for c in chosen:
        if c.get("package"):
            p = package_path(c)
            if p is None:
                errors.append(f"{c['id']}: package {c['package']} not installed")
                continue
            err = check(c, p)
        else:
            expected.add(os.path.normpath(c["path"]))
            err = check(c, os.path.join(a.dest, c["path"]))
        if err:
            errors.append(err)
    for root, _, files in os.walk(a.dest):
        for f in files:
            rel = os.path.normpath(os.path.relpath(os.path.join(root, f), a.dest))
            if rel in expected or f == ".gitkeep" or any(rel.startswith(p) for p in a.allow):
                continue
            errors.append(f"unexpected file under {a.dest}: {rel}")
    if a.stack == "public":
        errors += [f"internal-only package installed: {p}" for p in INTERNAL_ONLY_PACKAGES if importlib.util.find_spec(p)]
    if errors:
        sys.exit("model verification failed:\n  " + "\n  ".join(errors))
    print(f"verified {len(chosen)} checkpoints for the {a.stack} stack, nothing else under {a.dest}")


if __name__ == "__main__":
    main()
