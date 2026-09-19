import type { WavFormat } from './wav-header';

export type PcmSpec = Pick<WavFormat, 'channels' | 'bitsPerSample' | 'isFloat'>;

/**
 * Decode `frames` interleaved PCM frames starting at `byteOffset` of `src`
 * into planar Float32 output arrays (one per channel), writing from
 * `outOffset`. Integer formats are scaled to [-1, 1); float is copied.
 */
export function decodeInterleaved(
  src: ArrayBuffer,
  byteOffset: number,
  frames: number,
  spec: PcmSpec,
  out: Float32Array[],
  outOffset = 0,
): void {
  const { channels, bitsPerSample, isFloat } = spec;
  if (out.length < channels) throw new Error(`need ${channels} output arrays, got ${out.length}`);
  const bytesPerSample = bitsPerSample / 8;
  const needed = frames * channels * bytesPerSample;
  if (byteOffset + needed > src.byteLength) {
    throw new Error(`source too small: need ${needed} bytes at ${byteOffset}, have ${src.byteLength}`);
  }
  for (let c = 0; c < channels; c++) {
    if (out[c].length < outOffset + frames) throw new Error('output array too small');
  }

  if (isFloat) {
    if ((byteOffset & 3) === 0) {
      const f32 = new Float32Array(src, byteOffset, frames * channels);
      for (let c = 0; c < channels; c++) {
        const o = out[c];
        for (let i = 0, j = c; i < frames; i++, j += channels) o[outOffset + i] = f32[j];
      }
      return;
    }
    const view = new DataView(src, byteOffset);
    for (let c = 0; c < channels; c++) {
      const o = out[c];
      for (let i = 0; i < frames; i++) o[outOffset + i] = view.getFloat32((i * channels + c) * 4, true);
    }
    return;
  }

  const view = new DataView(src, byteOffset);
  if (bitsPerSample === 16) {
    for (let c = 0; c < channels; c++) {
      const o = out[c];
      for (let i = 0; i < frames; i++) o[outOffset + i] = view.getInt16((i * channels + c) * 2, true) / 32768;
    }
  } else if (bitsPerSample === 24) {
    const bytes = new Uint8Array(src, byteOffset);
    for (let c = 0; c < channels; c++) {
      const o = out[c];
      for (let i = 0; i < frames; i++) {
        const p = (i * channels + c) * 3;
        const v = (bytes[p] | (bytes[p + 1] << 8) | (bytes[p + 2] << 16)) << 8;
        o[outOffset + i] = (v >> 8) / 8388608;
      }
    }
  } else if (bitsPerSample === 32) {
    for (let c = 0; c < channels; c++) {
      const o = out[c];
      for (let i = 0; i < frames; i++) o[outOffset + i] = view.getInt32((i * channels + c) * 4, true) / 2147483648;
    }
  } else {
    throw new Error(`unsupported bit depth ${bitsPerSample}`);
  }
}
