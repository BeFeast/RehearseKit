import { describe, expect, it } from 'vitest';
import { columnsFor, mergeColumns, parsePeaks, PeaksError, stageFor } from '../peaks';

/**
 * Build a .pk buffer exactly as internal/pipeline/peaks.Write does:
 * header (21 bytes) + stage table (13 bytes each) + channel-major int8 pairs.
 */
function buildPk(opts: { channels: number; sampleRate: number; frames: number; stages: { shift: number; data: number[][] }[] }): ArrayBuffer {
  const HEADER = 21;
  const STAGE = 13;
  let dataBytes = 0;
  for (const st of opts.stages) dataBytes += st.data.reduce((n, ch) => n + ch.length, 0);
  const buf = new ArrayBuffer(HEADER + STAGE * opts.stages.length + dataBytes);
  const v = new DataView(buf);
  const u8 = new Uint8Array(buf);
  u8.set([0x52, 0x4b, 0x50, 0x4b], 0); // RKPK
  v.setUint16(4, 1, true);
  v.setUint16(6, opts.channels, true);
  v.setUint32(8, opts.sampleRate, true);
  v.setBigUint64(12, BigInt(opts.frames), true);
  v.setUint8(20, opts.stages.length);
  let offset = HEADER + STAGE * opts.stages.length;
  opts.stages.forEach((st, i) => {
    const rec = HEADER + i * STAGE;
    const numPeaks = st.data[0].length / 2;
    v.setUint8(rec, st.shift);
    v.setUint32(rec + 1, numPeaks, true);
    v.setBigUint64(rec + 5, BigInt(offset), true);
    for (const ch of st.data) {
      for (const x of ch) v.setInt8(offset++, x);
    }
  });
  return buf;
}

const sample = () =>
  buildPk({
    channels: 2,
    sampleRate: 48000,
    frames: 4 * 64,
    stages: [
      // 4 peaks of 64 frames; channel 0 louder than channel 1
      { shift: 6, data: [[-100, 100, -50, 50, -10, 10, 0, 0], [-20, 20, -20, 20, -20, 20, -20, 20]] },
      // one peak of 512 frames covering everything
      { shift: 9, data: [[-100, 100], [-20, 20]] },
    ],
  });

describe('parsePeaks', () => {
  it('reads the header, stage table and channel-major data', () => {
    const f = parsePeaks(sample());
    expect(f.channels).toBe(2);
    expect(f.sampleRate).toBe(48000);
    expect(f.frames).toBe(256);
    expect(f.stages).toHaveLength(2);
    expect(f.stages[0].shift).toBe(6);
    expect(f.stages[0].numPeaks).toBe(4);
    expect(f.stages[0].data[0][0]).toBe(-100);
    expect(f.stages[0].data[0][1]).toBe(100);
    expect(f.stages[0].data[1][7]).toBe(20);
    expect(f.stages[1].shift).toBe(9);
    expect(f.stages[1].numPeaks).toBe(1);
  });

  it('rejects bad magic, versions and truncation', () => {
    const buf = sample();
    new Uint8Array(buf)[0] = 0x58;
    expect(() => parsePeaks(buf)).toThrow(PeaksError);
    const v2 = sample();
    new DataView(v2).setUint16(4, 2, true);
    expect(() => parsePeaks(v2)).toThrow(/version/);
    expect(() => parsePeaks(sample().slice(0, 30))).toThrow(/truncated/);
    expect(() => parsePeaks(new ArrayBuffer(3))).toThrow(/small/);
  });
});

describe('stageFor', () => {
  it('picks the finest stage whose 2^shift fits the frames-per-pixel, like the Go reader', () => {
    const f = parsePeaks(sample());
    expect(stageFor(f, 10).shift).toBe(6); // none fit → finest
    expect(stageFor(f, 64).shift).toBe(6);
    expect(stageFor(f, 511).shift).toBe(6);
    expect(stageFor(f, 512).shift).toBe(9);
    expect(stageFor(f, 100000).shift).toBe(9);
  });
});

describe('columnsFor', () => {
  it('folds channels and peaks into drawable columns', () => {
    const f = parsePeaks(sample());
    const cols = columnsFor(f, 0, 256, 4);
    expect(cols).toHaveLength(4);
    expect(cols[0]).toEqual({ min: -100 / 127, max: 100 / 127 });
    expect(cols[1]).toEqual({ min: -50 / 127, max: 50 / 127 });
    expect(cols[2]).toEqual({ min: -20 / 127, max: 20 / 127 }); // channel 1 dominates
    expect(cols[3]).toEqual({ min: -20 / 127, max: 20 / 127 });
  });
  it('uses the coarse stage when many frames map to one column', () => {
    const f = parsePeaks(sample());
    const cols = columnsFor(f, 0, 256, 1);
    // 256 frames per pixel → stage 6 (2^9=512 does not fit) → union of all peaks
    expect(cols[0]).toEqual({ min: -100 / 127, max: 100 / 127 });
  });
  it('reads silence outside the file and handles zero columns', () => {
    const f = parsePeaks(sample());
    expect(columnsFor(f, 1000, 2000, 2)).toEqual([
      { min: 0, max: 0 },
      { min: 0, max: 0 },
    ]);
    expect(columnsFor(f, 0, 256, 0)).toEqual([]);
  });
  it('merges stems by summing and clamping', () => {
    const a = [{ min: -0.5, max: 0.5 }, { min: -0.9, max: 0.9 }];
    const b = [{ min: -0.5, max: 0.6 }, { min: -0.5, max: 0.5 }];
    expect(mergeColumns([a, b])).toEqual([
      { min: -1, max: 1 },
      { min: -1, max: 1 },
    ]);
    expect(mergeColumns([])).toEqual([]);
  });
});
