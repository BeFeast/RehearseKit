#!/usr/bin/env python3
"""run.py PREP_DIR OUT_DIR CONFIG... — separate every PREP_DIR/<song>/mix.wav with each config.

Configs:
  scnet          SCNet XL IHF on the mix (MSST)
  obsrnn         oBSRNN-SIMO on the mix (magronp/bsrnn)
  kim+scnet      Kim vocals, SCNet XL IHF on mix − vocals (public Plus HiFi chain)
  kim+obsrnn     Kim vocals, oBSRNN-SIMO on mix − vocals
  htdemucs_ft    current High preset, baseline only

In the chains the second model's own vocals estimate is folded into other, so the four stems still
sum to the mix. Writes OUT_DIR/<config>/<song>/{vocals,drums,bass,other}.wav and OUT_DIR/timing.json
(model load time, per-track seconds, real-time factor, peak VRAM allocated/reserved).

Paths come from the environment: MSST_DIR, BSRNN_DIR (code checkouts), MODELS_DIR (checkpoints).
"""
import glob, json, os, sys, time

import numpy as np
import soundfile as sf
import torch

SR = 44100
STEMS = ("vocals", "drums", "bass", "other")
DEV = torch.device("cuda")
MODELS = os.environ.get("MODELS_DIR", "/workspace/models")


def msst(model_type, config, ckpt):
    sys.path.insert(0, os.environ.get("MSST_DIR", "/workspace/msst"))
    from utils.settings import get_model_from_config
    from utils.model_utils import demix, prefer_target_instrument

    model, cfg = get_model_from_config(model_type, config)
    state = torch.load(ckpt, map_location="cpu", weights_only=False)
    for key in ("state", "state_dict"):
        if isinstance(state, dict) and key in state:
            state = state[key]
    model.load_state_dict(state)
    model = model.to(DEV).eval()
    names = prefer_target_instrument(cfg)

    def run(mix):  # mix: (2, n) float32
        with torch.inference_mode():
            out = demix(cfg, model, mix, DEV, model_type)
        if not isinstance(out, dict):
            out = {names[0]: out}
        return {k: np.asarray(v, dtype=np.float32) for k, v in out.items()}

    return run


def obsrnn(ckpt_dir):
    sys.path.insert(0, os.environ.get("BSRNN_DIR", "/workspace/bsrnn"))
    from hydra import compose, initialize_config_dir
    from models.separator import Separator

    with initialize_config_dir(config_dir=os.path.join(os.environ.get("BSRNN_DIR", "/workspace/bsrnn"), "conf"), version_base=None):
        args = compose("config", overrides=[f"ckpt_dir={ckpt_dir}", "model=simo-bsrnn-opt"])
    model = Separator(args)
    model.eval_device = DEV
    model.eval()
    order = list(model.model.targets)

    def run(mix):
        x = torch.from_numpy(mix).unsqueeze(0)
        peak = x.abs().max()
        with torch.inference_mode():
            est = model._apply_model_to_track(x / peak)[0].squeeze(0) * peak
        est = est.float().cpu().numpy()
        return {t: est[i] for i, t in enumerate(order)}

    return run


def htdemucs_ft():
    from demucs.pretrained import get_model
    from demucs.apply import apply_model

    bag = get_model("htdemucs_ft")
    bag.to(DEV).eval()

    def run(mix):  # the demucs CLI defaults rk uses: shifts=1, overlap=0.25, split
        x = torch.from_numpy(mix)
        ref = x.mean(0)
        m, s = ref.mean(), ref.std()
        with torch.inference_mode():
            est = apply_model(bag, ((x - m) / s)[None], shifts=1, split=True, overlap=0.25, device=DEV)[0]
        est = (est * s + m).cpu().numpy()
        return {t: est[i] for i, t in enumerate(bag.sources)}

    return run


def chain(first, second):
    def run(mix):
        v = first(mix)["vocals"]
        out = second(mix - v)
        out["other"] = out["other"] + out.pop("vocals")
        out["vocals"] = v
        return out

    return run


def build(name, cache):
    def get(key, make):
        if key not in cache:
            cache[key] = make()
        return cache[key]

    kim = lambda: get("kim", lambda: msst("mel_band_roformer", f"{MODELS}/config_vocals_mel_band_roformer_kj.yaml",
                                          f"{MODELS}/MelBandRoformer.ckpt"))
    scnet = lambda: get("scnet", lambda: msst("scnet", f"{MODELS}/scnet_xl_ihf.yaml", f"{MODELS}/scnet_xl_ihf.ckpt"))
    bsr = lambda: get("obsrnn", lambda: obsrnn(f"{MODELS}/simo-bsrnn-opt"))
    return {
        "scnet": scnet,
        "obsrnn": bsr,
        "kim+scnet": lambda: chain(kim(), scnet()),
        "kim+obsrnn": lambda: chain(kim(), bsr()),
        "htdemucs_ft": lambda: get("htdemucs_ft", htdemucs_ft),
    }[name]()


def main(prep, out, *configs):
    songs = sorted(os.path.basename(os.path.dirname(p)) for p in glob.glob(os.path.join(prep, "*", "mix.wav")))
    timing = {"gpu": torch.cuda.get_device_name(0), "torch": torch.__version__, "configs": {}}
    for name in configs:
        cache = {}  # one config at a time so peak VRAM is that config's own
        torch.cuda.empty_cache()
        torch.cuda.reset_peak_memory_stats()
        t0 = time.perf_counter()
        run = build(name, cache)
        torch.cuda.synchronize()
        rec = {"load_s": round(time.perf_counter() - t0, 1), "tracks": {}}
        for song in songs:
            mix, sr = sf.read(os.path.join(prep, song, "mix.wav"), dtype="float32", always_2d=True)
            assert sr == SR
            mix = np.ascontiguousarray(mix.T)
            torch.cuda.reset_peak_memory_stats()
            t0 = time.perf_counter()
            est = run(mix)
            torch.cuda.synchronize()
            dt = time.perf_counter() - t0
            dst = os.path.join(out, name, song)
            os.makedirs(dst, exist_ok=True)
            for s in STEMS:
                sf.write(os.path.join(dst, s + ".wav"), est[s].T[: mix.shape[1]], SR, subtype="FLOAT")
            dur = mix.shape[1] / SR
            rec["tracks"][song] = {
                "seconds": round(dt, 2), "audio_s": round(dur, 1), "x_realtime": round(dur / dt, 1),
                "vram_alloc_gb": round(torch.cuda.max_memory_allocated() / 2**30, 2),
                "vram_reserved_gb": round(torch.cuda.max_memory_reserved() / 2**30, 2),
            }
            print(name, song, rec["tracks"][song], flush=True)
        timing["configs"][name] = rec
        del run, cache
    with open(os.path.join(out, "timing.json"), "w") as f:
        json.dump(timing, f, indent=1)


if __name__ == "__main__":
    main(*sys.argv[1:])
