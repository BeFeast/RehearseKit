/**
 * Reader for RehearseKit waveform peak files (".pk"), written by
 * internal/pipeline/peaks (Go). Little-endian throughout:
 *
 *   magic "RKPK", u16 version=1, u16 channels, u32 sampleRate, u64 frames,
 *   u8 stages, then per stage { u8 shift; u32 numPeaks; u64 dataOffset }.
 *
 * Stage data is channel-major: for each channel, numPeaks pairs of
 * (int8 min, int8 max), scale round(clamp(x,-1,1)*127).
 */

export const PEAKS_MAGIC = 'RKPK';
export const PEAKS_VERSION = 1;

const HEADER_SIZE = 4 + 2 + 2 + 4 + 8 + 1; // 21
const STAGE_SIZE = 1 + 4 + 8; // 13

export interface PeaksStage {
  shift: number;
  numPeaks: number;
  dataOffset: number;
  /** Per channel: numPeaks pairs of (min, max). */
  data: Int8Array[];
}

export interface PeaksFile {
  channels: number;
  sampleRate: number;
  frames: number;
  stages: PeaksStage[];
}

export class PeaksError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'PeaksError';
  }
}

export function parsePeaks(buf: ArrayBuffer): PeaksFile {
  if (buf.byteLength < HEADER_SIZE) throw new PeaksError('peaks: file too small');
  const view = new DataView(buf);
  const magic = String.fromCharCode(view.getUint8(0), view.getUint8(1), view.getUint8(2), view.getUint8(3));
  if (magic !== PEAKS_MAGIC) throw new PeaksError('peaks: bad magic');
  const version = view.getUint16(4, true);
  if (version !== PEAKS_VERSION) throw new PeaksError(`peaks: unsupported version ${version}`);
  const channels = view.getUint16(6, true);
  const sampleRate = view.getUint32(8, true);
  const frames = Number(view.getBigUint64(12, true));
  const stageCount = view.getUint8(20);
  if (channels === 0 || stageCount === 0) throw new PeaksError('peaks: corrupt header');
  if (buf.byteLength < HEADER_SIZE + STAGE_SIZE * stageCount) throw new PeaksError('peaks: truncated stage table');
  const stages: PeaksStage[] = [];
  for (let i = 0; i < stageCount; i++) {
    const rec = HEADER_SIZE + i * STAGE_SIZE;
    const shift = view.getUint8(rec);
    const numPeaks = view.getUint32(rec + 1, true);
    const dataOffset = Number(view.getBigUint64(rec + 5, true));
    const bytes = 2 * numPeaks;
    if (dataOffset + bytes * channels > buf.byteLength) throw new PeaksError('peaks: truncated stage data');
    const data: Int8Array[] = [];
    for (let c = 0; c < channels; c++) {
      data.push(new Int8Array(buf, dataOffset + c * bytes, bytes));
    }
    stages.push({ shift, numPeaks, dataOffset, data });
  }
  return { channels, sampleRate, frames, stages };
}

/**
 * The finest stage whose frames-per-peak (2^shift) does not exceed
 * framesPerPixel, or the finest stage when none qualifies. Mirrors
 * peaks.File.StageFor on the server.
 */
export function stageFor(file: PeaksFile, framesPerPixel: number): PeaksStage {
  let best = file.stages[0];
  for (const st of file.stages) {
    if (Math.pow(2, st.shift) <= framesPerPixel) best = st;
  }
  return best;
}

export interface Column {
  /** −1..1 */
  min: number;
  /** −1..1 */
  max: number;
}

/**
 * Reduce a frame range to `columns` (min, max) pairs, all channels folded
 * together, for drawing. Frames outside the file read as silence.
 */
export function columnsFor(file: PeaksFile, startFrame: number, endFrame: number, columns: number): Column[] {
  const out: Column[] = [];
  if (columns <= 0) return out;
  const span = Math.max(1, endFrame - startFrame);
  const framesPerPixel = span / columns;
  const st = stageFor(file, framesPerPixel);
  const perPeak = Math.pow(2, st.shift);
  for (let x = 0; x < columns; x++) {
    const f0 = startFrame + (x * span) / columns;
    const f1 = startFrame + ((x + 1) * span) / columns;
    let p0 = Math.floor(f0 / perPeak);
    let p1 = Math.ceil(f1 / perPeak);
    if (p1 <= p0) p1 = p0 + 1;
    p0 = Math.max(0, p0);
    p1 = Math.min(st.numPeaks, p1);
    let mn = 0;
    let mx = 0;
    let any = false;
    for (let c = 0; c < file.channels; c++) {
      const d = st.data[c];
      for (let p = p0; p < p1; p++) {
        const lo = d[2 * p];
        const hi = d[2 * p + 1];
        if (!any) {
          mn = lo;
          mx = hi;
          any = true;
        } else {
          if (lo < mn) mn = lo;
          if (hi > mx) mx = hi;
        }
      }
    }
    out.push(any ? { min: mn / 127, max: mx / 127 } : { min: 0, max: 0 });
  }
  return out;
}

/** Element-wise union of several column sets (a mixed waveform from stems). */
export function mergeColumns(sets: Column[][]): Column[] {
  if (sets.length === 0) return [];
  const n = Math.min(...sets.map((s) => s.length));
  const out: Column[] = [];
  for (let i = 0; i < n; i++) {
    let mn = 0;
    let mx = 0;
    for (const s of sets) {
      // Summing stems approximates the mix; clamp to the drawable range.
      mn += s[i].min;
      mx += s[i].max;
    }
    out.push({ min: Math.max(-1, mn), max: Math.min(1, mx) });
  }
  return out;
}
