#!/usr/bin/env bash
# build-kit.sh — regenerate the RehearseKit "MIDI KIT audition" sample subset
# from sfzinstruments/virtuosity_drums (Versilian Studios + Karoryfer, CC0-1.0).
#
# Re-runnable: downloads only missing sources into src/<art>/, then rebuilds out/
# (FLAC 48 kHz, 16-bit, stereo kept where the source is stereo) and out/kit.json.
# Needs: bash, curl, ffmpeg/ffprobe (8.x), python3.
set -euo pipefail

REPO="https://github.com/sfzinstruments/virtuosity_drums"
COMMIT="9f04cf9a734527edfbb0a4eee1f674e45bbf71bc"   # master on 2026-09-28
RAW="https://raw.githubusercontent.com/sfzinstruments/virtuosity_drums/$COMMIT"

# In the repository the script lives in web/scripts/ and writes the kit the
# SPA serves; the source downloads (17 MB) go to a scratch dir outside git.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${KIT_OUT:-$HERE/../public/kit}"
SRC="${KIT_SRC:-${TMPDIR:-/tmp}/rk-kit-src}"
mkdir -p "$SRC" "$OUT"

FADE=0.03          # seconds of linear fade-out at the end of every sample
SIZE_LIMIT=4000000 # bytes, whole out/ directory

# Pitch shift used for the tom-mid substitution: high tom down 3 semitones.
# 48000 * 2^(-3/12) = 40363.03 -> asetrate=40363 then back to 48 kHz.
PRE_TOMM="asetrate=40363,aresample=48000,"
# Hi-hat pedal: skip the first 1000 samples (20.8 ms) of foot noise, mirroring
# offset=$HH_PPREROLL (keymap.sfz) applied to the pedal group in mappings/oh_all.sfz.
PRE_HHP="atrim=start=0.020833,asetpts=PTS-STARTPTS,"
prefilter() { case "$1" in TOMM) echo "$PRE_TOMM";; HHP) echo "$PRE_HHP";; *) echo "";; esac; }

# Manifest — one line per output sample:
#  art | layer | kit lovel | kit hivel | seconds | SFZ lovel | SFZ hivel | mic | srcdir | repo path | pre-filter
# "kit" velocity ranges are what kit.json exposes (contiguous 1..127);
# "SFZ" ranges are the region the source file occupies in the original mapping.
MANIFEST=$(cat <<'M'
kick |1|1  |63 |0.8|32 |63 |kickmic|kick |Samples/kickmic/kick/kickmic_kick_snon_vl2_rr1.flac|
kick |2|64 |95 |0.8|64 |95 |kickmic|kick |Samples/kickmic/kick/kickmic_kick_snon_vl3_rr1.flac|
kick |3|96 |127|0.8|96 |127|kickmic|kick |Samples/kickmic/kick/kickmic_kick_snon_vl4_rr1.flac|
snare|1|1  |63 |0.8|32 |34 |oh     |snare|Samples/oh/snare/oh_snare_center_vl10.flac|
snare|2|64 |95 |0.8|78 |80 |oh     |snare|Samples/oh/snare/oh_snare_center_vl23.flac|
snare|3|96 |127|0.8|117|119|oh     |snare|Samples/oh/snare/oh_snare_center_vl34.flac|
stick|1|1  |63 |0.8|32 |39 |oh     |stick|Samples/oh/snare/oh_snare_crossstick_vl5.flac|
stick|2|64 |95 |0.8|80 |87 |oh     |stick|Samples/oh/snare/oh_snare_crossstick_vl11.flac|
stick|3|96 |127|0.8|112|119|oh     |stick|Samples/oh/snare/oh_snare_crossstick_vl15.flac|
hhc  |1|1  |63 |0.8|32 |63 |oh     |hhc  |Samples/oh/hh/oh_hh_closed_vl2_rr1.flac|
hhc  |2|64 |95 |0.8|64 |95 |oh     |hhc  |Samples/oh/hh/oh_hh_closed_vl3_rr1.flac|
hhc  |3|96 |127|0.8|96 |127|oh     |hhc  |Samples/oh/hh/oh_hh_closed_vl4_rr1.flac|
hho  |1|1  |63 |1.5|32 |63 |oh     |hho  |Samples/oh/hh/oh_hh_open_vl2_rr1.flac|
hho  |2|64 |95 |1.5|64 |95 |oh     |hho  |Samples/oh/hh/oh_hh_open_vl3_rr1.flac|
hho  |3|96 |127|1.5|96 |127|oh     |hho  |Samples/oh/hh/oh_hh_open_vl4_rr1.flac|
hhp  |1|1  |42 |0.8|1  |42 |oh     |hhp  |Samples/oh/hh/oh_hh_pedal_vl1_rr1.flac|HHP
hhp  |2|43 |85 |0.8|43 |85 |oh     |hhp  |Samples/oh/hh/oh_hh_pedal_vl2_rr1.flac|HHP
hhp  |3|86 |127|0.8|86 |127|oh     |hhp  |Samples/oh/hh/oh_hh_pedal_vl3_rr1.flac|HHP
tomh |1|1  |63 |0.8|32 |39 |oh     |tomh |Samples/oh/htom/oh_htom_center_vl5.flac|
tomh |2|64 |95 |0.8|80 |87 |oh     |tomh |Samples/oh/htom/oh_htom_center_vl11.flac|
tomh |3|96 |127|0.8|112|119|oh     |tomh |Samples/oh/htom/oh_htom_center_vl15.flac|
tomm |1|1  |63 |0.8|32 |39 |oh     |tomh |Samples/oh/htom/oh_htom_center_vl5.flac|TOMM
tomm |2|64 |95 |0.8|80 |87 |oh     |tomh |Samples/oh/htom/oh_htom_center_vl11.flac|TOMM
tomm |3|96 |127|0.8|112|119|oh     |tomh |Samples/oh/htom/oh_htom_center_vl15.flac|TOMM
tomf |1|1  |63 |0.8|32 |39 |oh     |tomf |Samples/oh/ltom/oh_ltom_center_vl5.flac|
tomf |2|64 |95 |0.8|80 |87 |oh     |tomf |Samples/oh/ltom/oh_ltom_center_vl11.flac|
tomf |3|96 |127|0.8|112|119|oh     |tomf |Samples/oh/ltom/oh_ltom_center_vl15.flac|
ride |1|1  |42 |2.0|1  |42 |oh     |ride |Samples/oh/ride/oh_ride_ride_vl1_rr1.flac|
ride |2|43 |85 |2.0|43 |85 |oh     |ride |Samples/oh/ride/oh_ride_ride_vl2_rr1.flac|
ride |3|86 |127|2.0|86 |127|oh     |ride |Samples/oh/ride/oh_ride_ride_vl3_rr1.flac|
bell |1|1  |42 |2.0|1  |42 |oh     |bell |Samples/oh/ride/oh_ride_bell_vl1_rr1.flac|
bell |2|43 |85 |2.0|43 |85 |oh     |bell |Samples/oh/ride/oh_ride_bell_vl2_rr1.flac|
bell |3|86 |127|2.0|86 |127|oh     |bell |Samples/oh/ride/oh_ride_bell_vl3_rr1.flac|
crash|1|1  |42 |3.0|1  |42 |oh     |crash|Samples/oh/crash/oh_crash_crash_vl1_rr1.flac|
crash|2|43 |85 |3.0|43 |85 |oh     |crash|Samples/oh/crash/oh_crash_crash_vl2_rr1.flac|
crash|3|86 |127|3.0|86 |127|oh     |crash|Samples/oh/crash/oh_crash_crash_vl3_rr1.flac|
M
)
ARTS="kick snare stick hhc hho hhp tomh tomm tomf ride bell crash"

trim() { echo "$1" | tr -d ' '; }
urlenc() { python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1]))' "$1"; }
peak_db() { # peak_db <file> <prefilter>  -> max_volume in dB (float)
  ffmpeg -hide_banner -nostats -i "$1" -af "${2}volumedetect" -f null - 2>&1 \
    | sed -n 's/.*max_volume: \([-0-9.]*\) dB.*/\1/p'
}

echo "== 1/4 download sources (skip existing)"
while IFS='|' read -r art layer lo hi len slo shi mic srcdir path pre; do
  srcdir=$(trim "$srcdir"); path=$(trim "$path")
  dest="$SRC/$srcdir/$(basename "$path")"
  mkdir -p "$SRC/$srcdir"
  if [ ! -s "$dest" ]; then
    echo "   GET $path"
    curl -sSL --fail --retry 3 -o "$dest" "$RAW/$(urlenc "$path")"
  fi
done <<<"$MANIFEST"
[ -s "$OUT/LICENSE.txt" ] || curl -sSL --fail -o "$OUT/LICENSE.txt" "$RAW/LICENSE"

echo "== 2/4 measure peaks, compute one gain per articulation (hardest layer -> -1 dBFS)"
declare -A GAIN
for art in $ARTS; do
  maxpk=-999
  while IFS='|' read -r a layer lo hi len slo shi mic srcdir path pre; do
    [ "$(trim "$a")" = "$art" ] || continue
    f="$SRC/$(trim "$srcdir")/$(basename "$(trim "$path")")"
    p=$(prefilter "$(trim "$pre")")
    pk=$(peak_db "$f" "$p")
    maxpk=$(python3 -c "print(max($maxpk,$pk))")
  done <<<"$MANIFEST"
  GAIN[$art]=$(python3 -c "print(round(-1.0 - ($maxpk), 2))")
  printf "   %-6s group peak %7.2f dB -> volume=%+.2f dB\n" "$art" "$maxpk" "${GAIN[$art]}"
done

echo "== 3/4 render out/<art>-<layer>.flac"
rm -f "$OUT"/*.flac
ROWS="" # for kit.json
while IFS='|' read -r art layer lo hi len slo shi mic srcdir path pre; do
  art=$(trim "$art"); layer=$(trim "$layer"); lo=$(trim "$lo"); hi=$(trim "$hi")
  len=$(trim "$len"); slo=$(trim "$slo"); shi=$(trim "$shi"); mic=$(trim "$mic")
  srcdir=$(trim "$srcdir"); path=$(trim "$path"); pre=$(trim "$pre")
  in="$SRC/$srcdir/$(basename "$path")"; outf="$OUT/$art-$layer.flac"
  p=$(prefilter "$pre")
  fst=$(python3 -c "print(round($len-$FADE,3))")
  # chain: [pitch | pedal pre-trim] -> group gain -> strip leading silence (<-60 dBFS) -> cut to length
  #        -> pad to exactly <len> if shorter -> 30 ms linear fade-out -> 16-bit FLAC
  ffmpeg -hide_banner -loglevel error -y -i "$in" \
    -af "${p}volume=${GAIN[$art]}dB,silenceremove=start_periods=1:start_threshold=-60dB,atrim=0:${len},apad=whole_dur=${len},afade=t=out:st=${fst}:d=${FADE}" \
    -ar 48000 -sample_fmt s16 -c:a flac -compression_level 8 "$outf"
  bytes=$(stat -c %s "$outf")
  dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$outf")
  ch=$(ffprobe -v error -select_streams a:0 -show_entries stream=channels -of csv=p=0 "$outf")
  opk=$(peak_db "$outf" "")
  printf "   %-12s %4.2fs %2sch %7d B  peak %6.2f dB  <- %s [%s-%s] %s\n" "$(basename "$outf")" "$dur" "$ch" "$bytes" "$opk" "$(basename "$path")" "$slo" "$shi" "$mic"
  ROWS+="$art|$layer|$lo|$hi|$path|$slo|$shi|$mic|$dur|$bytes|$ch|$opk|${GAIN[$art]}"$'\n'
done <<<"$MANIFEST"

echo "== 4/4 kit.json + size check"
TOTAL=$(du -sb "$OUT" --exclude=kit.json --exclude=LICENSE.txt | cut -f1)
python3 - "$OUT/kit.json" "$REPO" "$COMMIT" "$TOTAL" <<'PY' "$ROWS"
import sys, json
out, repo, commit, total, rows = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4]), sys.argv[5]
arts = {}
for line in rows.strip().splitlines():
    art, layer, lo, hi, path, slo, shi, mic, dur, byts, ch, opk, gain = line.split('|')
    arts.setdefault(art, []).append({
        "file": f"{art}-{layer}.flac", "vel": [int(lo), int(hi)], "gain": 1.0,
        "src": path, "srcVel": [int(slo), int(shi)], "mic": mic,
        "channels": int(ch), "seconds": round(float(dur), 3), "bytes": int(byts),
        "peakDbfs": float(opk), "appliedGainDb": float(gain),
    })
kit = {
  "name": "Virtuosity Drums (subset)",
  "license": "CC0-1.0",
  "source": repo,
  "commit": commit,
  "sampleRate": 48000,
  "format": "flac, 16-bit, 48 kHz; stereo where the source mic pair is stereo (oh), mono for kickmic",
  "mic": {"default": "oh", "kick": "kickmic",
          "note": "oh = stereo overhead pair (Samples/oh). The kick was taken from the close mic (Samples/kickmic, mono): on the overheads the hardest kick peaks at -20.7 dBFS vs -0.4 for the snare and is mostly room, so normalising it would lift rumble and noise by 20 dB."},
  "velocityLayers": "3 per articulation. 4-layer sources (kick, hhc, hho): vl2/vl3/vl4 kept, ghost layer vl1 dropped and its range folded into layer 1. 16-layer sources (stick, tomh, tomf): vl5/vl11/vl15. 36-layer snare center: vl10/vl23/vl34. Native 3-layer sources (hhp, ride, bell, crash): all three, first round-robin.",
  "normalisation": "per articulation: one gain so the hardest layer peaks at -1 dBFS; softer layers keep their relative level (see appliedGainDb / peakDbfs)",
  "trim": {"leadingSilence": "silenceremove start_threshold=-60dB", "fadeOut": "30 ms linear",
           "hhpOffset": "first 1000 samples (20.8 ms) of the pedal samples skipped, as the library does with offset=$HH_PPREROLL in Programs/mappings/oh_all.sfz",
           "seconds": {"kick": 0.8, "snare": 0.8, "stick": 0.8, "hhc": 0.8, "hhp": 0.8, "tomh": 0.8, "tomm": 0.8, "tomf": 0.8, "hho": 1.5, "ride": 2.0, "bell": 2.0, "crash": 3.0}},
  "articulations": {a: arts[a] for a in ["kick","snare","stick","hhc","hho","hhp","tomh","tomm","tomf","ride","bell","crash"]},
  "substitutions": {
    "tomm": "kit has only two toms (htom, ltom); tomm = htom center hit pitched down 3 semitones (ffmpeg asetrate=40363,aresample=48000; same source files as tomh)",
    "tomf": "ltom (the kit's low/floor tom) used as-is",
    "stick": "snare cross-stick articulation (snare_crossstick) used for GM side stick"
  },
  "totalBytes": total,
}
json.dump(kit, open(out, "w"), indent=1)
print(f"   wrote {out}")
PY
TOTAL=$(du -sb "$OUT" --exclude=LICENSE.txt | cut -f1)
echo "   out/ total: $TOTAL bytes (limit $SIZE_LIMIT)"
[ "$TOTAL" -le "$SIZE_LIMIT" ] || { echo "ERROR: out/ exceeds size limit" >&2; exit 1; }
echo "== done"
