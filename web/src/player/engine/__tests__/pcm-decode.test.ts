import { describe, expect, it } from 'vitest';
import { decodeInterleaved } from '../pcm-decode';
import { encodeSamples } from '../testing/wav-builder';

const planar = (channels: number, frames: number) =>
  Array.from({ length: channels }, () => new Float32Array(frames));

describe('decodeInterleaved', () => {
  const values = [
    [0, 0.5, -0.5, 0.25, -1],
    [1, -0.25, 0.125, 0, 0.75],
  ];

  it('decodes 16-bit stereo with 1/32768 scaling', () => {
    const pcm = encodeSamples(values, 16, false);
    const out = planar(2, 5);
    decodeInterleaved(pcm, 0, 5, { channels: 2, bitsPerSample: 16, isFloat: false }, out);
    expect(out[0][1]).toBeCloseTo(0.5, 4);
    expect(out[0][4]).toBeCloseTo(-32767 / 32768, 6);
    expect(out[1][0]).toBeCloseTo(32767 / 32768, 6);
    expect(out[1][2]).toBeCloseTo(0.125, 4);
  });

  it('decodes 24-bit with sign extension', () => {
    const pcm = encodeSamples(values, 24, false);
    const out = planar(2, 5);
    decodeInterleaved(pcm, 0, 5, { channels: 2, bitsPerSample: 24, isFloat: false }, out);
    expect(out[0][2]).toBeCloseTo(-0.5, 5);
    expect(out[0][4]).toBeCloseTo(-8388607 / 8388608, 7);
    expect(out[1][4]).toBeCloseTo(0.75, 5);
    expect(out[1][1]).toBeCloseTo(-0.25, 5);
  });

  it('decodes 32-bit integer', () => {
    const pcm = encodeSamples(values, 32, false);
    const out = planar(2, 5);
    decodeInterleaved(pcm, 0, 5, { channels: 2, bitsPerSample: 32, isFloat: false }, out);
    expect(out[0][1]).toBeCloseTo(0.5, 7);
    expect(out[1][0]).toBeCloseTo(2147483647 / 2147483648, 9);
  });

  it('copies 32-bit float exactly, aligned and unaligned', () => {
    const pcm = encodeSamples(values, 32, true);
    const out = planar(2, 5);
    decodeInterleaved(pcm, 0, 5, { channels: 2, bitsPerSample: 32, isFloat: true }, out);
    expect(Array.from(out[0])).toEqual(values[0]);
    expect(Array.from(out[1])).toEqual(values[1]);

    const shifted = new ArrayBuffer(pcm.byteLength + 2);
    new Uint8Array(shifted, 2).set(new Uint8Array(pcm));
    const out2 = planar(2, 5);
    decodeInterleaved(shifted, 2, 5, { channels: 2, bitsPerSample: 32, isFloat: true }, out2);
    expect(Array.from(out2[1])).toEqual(values[1]);
  });

  it('decodes mono and honours outOffset', () => {
    const pcm = encodeSamples([values[0]], 16, false);
    const out = planar(1, 8);
    decodeInterleaved(pcm, 0, 5, { channels: 1, bitsPerSample: 16, isFloat: false }, out, 3);
    expect(out[0][0]).toBe(0);
    expect(out[0][4]).toBeCloseTo(0.5, 4);
  });

  it('decodes a sub-range starting at a byte offset', () => {
    const pcm = encodeSamples(values, 16, false);
    const out = planar(2, 2);
    decodeInterleaved(pcm, 3 * 4, 2, { channels: 2, bitsPerSample: 16, isFloat: false }, out);
    expect(out[0][0]).toBeCloseTo(0.25, 4);
    expect(out[1][1]).toBeCloseTo(0.75, 4);
  });

  it('rejects short sources, short outputs and odd depths', () => {
    const pcm = encodeSamples(values, 16, false);
    expect(() =>
      decodeInterleaved(pcm, 0, 6, { channels: 2, bitsPerSample: 16, isFloat: false }, planar(2, 6)),
    ).toThrow(/source too small/);
    expect(() =>
      decodeInterleaved(pcm, 0, 5, { channels: 2, bitsPerSample: 16, isFloat: false }, planar(2, 4)),
    ).toThrow(/output array too small/);
    expect(() =>
      decodeInterleaved(pcm, 0, 5, { channels: 2, bitsPerSample: 16, isFloat: false }, planar(1, 5)),
    ).toThrow(/output arrays/);
    expect(() =>
      decodeInterleaved(pcm, 0, 5, { channels: 2, bitsPerSample: 8 as unknown as 16, isFloat: false }, planar(2, 5)),
    ).toThrow(/unsupported bit depth/);
  });
});
