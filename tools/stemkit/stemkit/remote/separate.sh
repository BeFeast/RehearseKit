#!/usr/bin/env bash
# GPU-side 5-stem separation. Runs inside the vast.ai container (pytorch CUDA image), invoked by stemkit.remote.
#
#   1. vocals       <- MelBand Roformer "Kim" (audio-separator, vocal SDR 12.6)
#   2. instrumental  = (mix - vocals) * 0.5   (headroom: the difference can exceed 0 dBFS, PCM_24 would clip)
#   3. instrumental -> BS-Rofo-SW 6-stem BS-RoFormer (bass/drums/other/vocals/guitar/piano) via ZFTurbo MSST, TTA on
#   4. other         = other + piano + SW's vocal residual + (mix - sum)   => the 5 stems sum to the mix exactly
#
# Usage: separate.sh /workspace/in/mix.flac /workspace/stems
set -euo pipefail
IN="$1"; OUT="$2"; W=/workspace
mkdir -p "$W/in" "$W/in_B" "$W/models" "$OUT"
[ "$IN" = "$W/in/mix.flac" ] || cp "$IN" "$W/in/mix.flac"

log() { echo "[separate] $(date -u +%H:%M:%S) $*"; }

log "installing deps"
export DEBIAN_FRONTEND=noninteractive
apt-get install -y -qq git ffmpeg build-essential >/dev/null 2>&1
[ -d "$W/msst" ] || git clone -q https://github.com/ZFTurbo/Music-Source-Separation-Training.git "$W/msst"
# MSST requirements.txt pulls wxpython/pyaudio that fail to build; install only what inference needs.
pip install -q numpy pandas scipy soundfile ml_collections tqdm omegaconf==2.2.3 beartype==0.14.1 \
  rotary_embedding_torch==0.3.5 einops==0.8.1 librosa matplotlib audiomentations==0.24.0 "pedalboard~=0.8.1" \
  auraloss torch_log_wmse torch_audiomentations pytorch_optimizer hyper_connections==0.1.11 \
  segmentation_models_pytorch==0.3.3 timm==0.9.2 torchmetrics==0.11.4 huggingface-hub
# Installed separately and pinned: resolved together with the pins above, pip picked an ancient audio-separator
# whose CLI has no -m/--model_filename.
pip install -q "audio-separator[gpu]==0.47.0"
log "audio-separator $(audio-separator --version 2>&1 | tail -1)"

# Original jarredou/BS-ROFO-SW-Fixed repo is gone from HF; the enerjazzer mirror has the identical ckpt (699412152 bytes).
log "fetching BS-Rofo-SW"
cd "$W/models"
[ -f BS-Rofo-SW-Fixed.ckpt ] || curl -sLO https://huggingface.co/enerjazzer/BS-ROFO-SW-Fixed/resolve/main/BS-Rofo-SW-Fixed.ckpt
[ -f BS-Rofo-SW-Fixed.yaml ] || curl -sLO https://huggingface.co/enerjazzer/BS-ROFO-SW-Fixed/resolve/main/BS-Rofo-SW-Fixed.yaml
[ "$(stat -c %s BS-Rofo-SW-Fixed.ckpt)" = "699412152" ] || { echo "ckpt size mismatch"; exit 2; }

log "1/4 vocals (MelBand Roformer Kim)"
audio-separator "$W/in/mix.flac" --model_filename vocals_mel_band_roformer.ckpt --output_dir "$W/voc" \
  --output_format FLAC 2>&1 | grep -iE "error|Traceback|complete" || true
ls "$W"/voc/*_\(vocals\)_*.flac >/dev/null 2>&1 || { echo "vocals step produced no output"; exit 3; }

log "2/4 instrumental"
python - <<'EOF'
import soundfile as sf, glob
mix,sr=sf.read("/workspace/in/mix.flac",dtype="float64")
voc,_=sf.read(glob.glob("/workspace/voc/*_(vocals)_*.flac")[0],dtype="float64")
sf.write("/workspace/in_B/mix.flac",(mix-voc)*0.5,sr,subtype="PCM_24")
EOF

log "3/4 6-stem split (BS-Rofo-SW + TTA)"
cd "$W/msst"
python inference.py --model_type bs_roformer --config_path "$W/models/BS-Rofo-SW-Fixed.yaml" \
  --start_check_point "$W/models/BS-Rofo-SW-Fixed.ckpt" --input_folder "$W/in_B" --store_dir "$W/sw6" \
  --use_tta --flac_file --pcm_type PCM_24 --disable_detailed_pbar 2>&1 | grep -E "Elapsed|Error|Traceback" || true
for s in bass drums other vocals guitar piano; do
  ls "$W"/sw6/mix/$s.* >/dev/null 2>&1 || { echo "6-stem step missing $s"; exit 4; }
done

log "4/4 assemble 32-bit float stems"
python - "$OUT" <<'EOF'
import soundfile as sf, numpy as np, glob, sys, json
out=sys.argv[1]
rd=lambda p,s=1.0: sf.read(p,dtype="float64")[0]*s
mix,sr=sf.read("/workspace/in/mix.flac",dtype="float64")
voc=rd(glob.glob("/workspace/voc/*_(vocals)_*.flac")[0])
B="/workspace/sw6/mix/"
def stem(n):  # MSST falls back to .wav when a stem peaks above 1.0
    return rd((glob.glob(B+n+".flac")+glob.glob(B+n+".wav"))[0],2)
drums,bass,guitar=stem("drums"),stem("bass"),stem("guitar")
other=stem("other")+stem("piano")+stem("vocals")
other+=mix-(voc+drums+bass+guitar+other)
report={}
for n,x in [("vocals",voc),("drums",drums),("bass",bass),("guitar",guitar),("other",other)]:
    sf.write(f"{out}/{n}.wav",x.astype(np.float32),sr,subtype="FLOAT")
    report[n]={"peak":float(np.abs(x).max()),"rms_db":float(20*np.log10(np.sqrt(np.mean(x**2))+1e-12))}
json.dump({"sample_rate":sr,"frames":len(mix),"stems":report},open(f"{out}/report.json","w"),indent=1)
print(json.dumps(report))
EOF
log "done"
