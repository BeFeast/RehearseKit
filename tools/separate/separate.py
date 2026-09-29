#!/usr/bin/env python3
"""separate.py — run one MSST separation recipe from internal/models/manifest.json.

  separate.py --manifest M --models-dir /models --recipe ID --stack public|internal
              --input source.wav --out DIR [--device cuda]

Writes DIR/<stem>.wav (32-bit float, 44.1 kHz) for every stem of the recipe and prints
"progress <0..1>" lines on stderr for the Go agent.

Recipes with one model return its stems. Recipes with two models are vocal chains: the
first model (MelBand RoFormer, Kim) takes the vocals from the mix, the second separates
mix − vocals, and its own vocals estimate is folded into other so the stems still sum to
the mix. Recipes marked "tta" average the second model over channel-swapped and
polarity-inverted copies (MSST apply_tta), as the stemkit HiFi run did.

Nothing is downloaded: every file is read from --models-dir and refused if its class is not
allowed for --stack or its size differs from the manifest. The Go agent verified every
sha256 at startup (models.Scan) and checked the recipe against the job's stack
(models.Check); this is the last lock before torch.load.
"""
import argparse, json, os, sys

SR = 44100


def progress(p):
    print(f"progress {p:.3f}", file=sys.stderr, flush=True)


def allowed(stack, cls):
    return cls == "public" or (cls == "internal" and stack == "internal")


def resolve(manifest, models_dir, cid, stack):
    c = next((c for c in manifest["checkpoints"] if c["id"] == cid), None)
    if c is None:
        sys.exit(f"unknown checkpoint {cid}")
    if not allowed(stack, c["class"]):
        sys.exit(f"checkpoint {cid} is {c['class']}, not allowed on the {stack} stack")
    path = os.path.join(models_dir, c["path"])
    if os.path.getsize(path) != c["size"]:
        sys.exit(f"{path} does not match the manifest size")
    return c, path


def load_model(manifest, models_dir, cid, stack, device):
    import torch
    from utils.settings import get_model_from_config

    c, ckpt = resolve(manifest, models_dir, cid, stack)
    _, cfg_path = resolve(manifest, models_dir, c["config"], stack)
    model, cfg = get_model_from_config(c["model_type"], cfg_path)
    state = torch.load(ckpt, map_location="cpu", weights_only=False)
    for key in ("state", "state_dict"):
        if isinstance(state, dict) and key in state:
            state = state[key]
    model.load_state_dict(state)
    return model.to(device).eval(), cfg, c["model_type"]


def run_model(model, cfg, model_type, mix, device, tta):
    import numpy as np
    import torch
    from utils.model_utils import apply_tta, demix, prefer_target_instrument

    with torch.inference_mode():
        out = demix(cfg, model, mix, device, model_type)
        if not isinstance(out, dict):
            out = {prefer_target_instrument(cfg)[0]: out}
        if tta:
            out = apply_tta(cfg, model, mix, out, device, model_type)
    return {k: np.asarray(v, dtype=np.float32) for k, v in out.items()}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--manifest", required=True)
    ap.add_argument("--models-dir", required=True)
    ap.add_argument("--recipe", required=True)
    ap.add_argument("--stack", required=True, choices=["public", "internal"])
    ap.add_argument("--input", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--device", default="cuda")
    a = ap.parse_args()

    manifest = json.load(open(a.manifest))
    recipe = next((r for r in manifest["recipes"] if r["id"] == a.recipe), None)
    if recipe is None or recipe["engine"] != "msst":
        sys.exit(f"no msst recipe {a.recipe}")
    steps = [cid for cid in recipe["checkpoints"]
             if next(c for c in manifest["checkpoints"] if c["id"] == cid).get("model_type")]
    stems = ["vocals", "drums", "bass", "other"] + (["guitar", "piano"] if recipe["stems"] == 6 else [])

    import librosa
    import numpy as np
    import soundfile as sf
    import torch

    # An explicit index: MSST asks torch.cuda.mem_get_info(device), which on torch 2.2
    # rejects a bare "cuda".
    device = torch.device("cuda:0" if a.device.startswith("cuda") and torch.cuda.is_available() else "cpu")
    if a.device.startswith("cuda:"):
        device = torch.device(a.device)
    mix, _ = librosa.load(a.input, sr=SR, mono=False)
    if mix.ndim == 1:
        mix = np.stack([mix, mix])
    mix = np.ascontiguousarray(mix, dtype=np.float32)
    progress(0.02)

    if len(steps) == 1:
        model, cfg, mt = load_model(manifest, a.models_dir, steps[0], a.stack, device)
        progress(0.1)
        out = run_model(model, cfg, mt, mix, device, recipe.get("tta", False))
    elif len(steps) == 2:
        model, cfg, mt = load_model(manifest, a.models_dir, steps[0], a.stack, device)
        progress(0.05)
        vocals = run_model(model, cfg, mt, mix, device, False)["vocals"]
        del model
        torch.cuda.empty_cache()
        progress(0.35)
        model, cfg, mt = load_model(manifest, a.models_dir, steps[1], a.stack, device)
        progress(0.4)
        out = run_model(model, cfg, mt, mix - vocals, device, recipe.get("tta", False))
        out["other"] = out["other"] + out.pop("vocals")
        out["vocals"] = vocals
    else:
        sys.exit(f"recipe {a.recipe}: {len(steps)} models, expected 1 or 2")

    os.makedirs(a.out, exist_ok=True)
    for s in stems:
        if s not in out:
            sys.exit(f"recipe {a.recipe} produced no {s} stem (got {sorted(out)})")
        sf.write(os.path.join(a.out, s + ".wav"), out[s].T, SR, subtype="FLOAT")
    progress(1.0)


if __name__ == "__main__":
    sys.path.insert(0, os.environ.get("RK_MSST_DIR", "/opt/msst"))
    main()
