/** Synthetic WAV builders for tests. */

export interface BuildOptions {
  channels: number;
  sampleRate: number;
  bitsPerSample: 16 | 24 | 32;
  float?: boolean;
  extensible?: boolean;
  /** Extra chunks inserted before the data chunk: [id, bodyBytes]. */
  extraChunks?: Array<[string, number]>;
  /** Override the data chunk size field (e.g. 0xffffffff placeholder). */
  dataSizeField?: number;
}

function writeFourcc(view: DataView, offset: number, id: string): void {
  for (let i = 0; i < 4; i++) view.setUint8(offset + i, id.charCodeAt(i));
}

/** Encode planar float samples into an interleaved PCM byte buffer. */
export function encodeSamples(planar: number[][], bits: 16 | 24 | 32, float: boolean): ArrayBuffer {
  const channels = planar.length;
  const frames = planar[0].length;
  const bytesPer = bits / 8;
  const buf = new ArrayBuffer(frames * channels * bytesPer);
  const view = new DataView(buf);
  for (let i = 0; i < frames; i++) {
    for (let c = 0; c < channels; c++) {
      const v = planar[c][i];
      const p = (i * channels + c) * bytesPer;
      if (float) view.setFloat32(p, v, true);
      else if (bits === 16) view.setInt16(p, Math.round(v * 32767), true);
      else if (bits === 32) view.setInt32(p, Math.round(v * 2147483647), true);
      else {
        const s = Math.round(v * 8388607);
        view.setUint8(p, s & 0xff);
        view.setUint8(p + 1, (s >> 8) & 0xff);
        view.setUint8(p + 2, (s >> 16) & 0xff);
      }
    }
  }
  return buf;
}

export function buildWav(opts: BuildOptions, pcm: ArrayBuffer): { buffer: ArrayBuffer; dataOffset: number } {
  const fmtSize = opts.extensible ? 40 : 16;
  const extras = opts.extraChunks ?? [];
  let extraBytes = 0;
  for (const [, n] of extras) extraBytes += 8 + n + (n & 1);
  const dataOffset = 12 + 8 + fmtSize + extraBytes + 8;
  const total = dataOffset + pcm.byteLength;
  const buf = new ArrayBuffer(total);
  const view = new DataView(buf);
  writeFourcc(view, 0, 'RIFF');
  view.setUint32(4, total - 8, true);
  writeFourcc(view, 8, 'WAVE');
  let off = 12;
  writeFourcc(view, off, 'fmt ');
  view.setUint32(off + 4, fmtSize, true);
  const tag = opts.float ? 3 : 1;
  view.setUint16(off + 8, opts.extensible ? 0xfffe : tag, true);
  view.setUint16(off + 10, opts.channels, true);
  view.setUint32(off + 12, opts.sampleRate, true);
  const blockAlign = (opts.channels * opts.bitsPerSample) / 8;
  view.setUint32(off + 16, opts.sampleRate * blockAlign, true);
  view.setUint16(off + 20, blockAlign, true);
  view.setUint16(off + 22, opts.bitsPerSample, true);
  if (opts.extensible) {
    view.setUint16(off + 24, 22, true); // cbSize
    view.setUint16(off + 26, opts.bitsPerSample, true); // valid bits
    view.setUint32(off + 28, opts.channels === 2 ? 3 : 4, true); // channel mask
    view.setUint16(off + 32, tag, true); // SubFormat GUID leading tag
  }
  off += 8 + fmtSize;
  for (const [id, n] of extras) {
    writeFourcc(view, off, id);
    view.setUint32(off + 4, n, true);
    off += 8 + n + (n & 1);
  }
  writeFourcc(view, off, 'data');
  view.setUint32(off + 4, opts.dataSizeField ?? pcm.byteLength, true);
  off += 8;
  new Uint8Array(buf, off).set(new Uint8Array(pcm));
  return { buffer: buf, dataOffset };
}
