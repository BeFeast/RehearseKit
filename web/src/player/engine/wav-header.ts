/**
 * Minimal RIFF/WAVE header parser. Parsed once per stem from the first range
 * request; afterwards every chunk is addressed by byte offset:
 * `dataOffset + frame * blockAlign`.
 */

export interface WavFormat {
  /** 1 = PCM integer, 3 = IEEE float (resolved through WAVE_FORMAT_EXTENSIBLE). */
  formatTag: 1 | 3;
  channels: number;
  sampleRate: number;
  bitsPerSample: number;
  blockAlign: number;
  isFloat: boolean;
  /** Absolute byte offset of the first audio frame. */
  dataOffset: number;
  /** Bytes of audio data (clamped to the file size when known). */
  dataBytes: number;
  totalFrames: number;
}

export const HEADER_PROBE_BYTES = 65536;

export class WavHeaderError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'WavHeaderError';
  }
}

const FORMAT_PCM = 1;
const FORMAT_IEEE_FLOAT = 3;
const FORMAT_EXTENSIBLE = 0xfffe;

function fourcc(view: DataView, offset: number): string {
  return String.fromCharCode(
    view.getUint8(offset),
    view.getUint8(offset + 1),
    view.getUint8(offset + 2),
    view.getUint8(offset + 3),
  );
}

/**
 * Parse the header found in `buf` (the first bytes of the file). `fileSize`,
 * when known (from Content-Range), clamps the data chunk size, which some
 * writers leave as a placeholder.
 */
export function parseWavHeader(buf: ArrayBuffer, fileSize?: number): WavFormat {
  const view = new DataView(buf);
  if (buf.byteLength < 12) throw new WavHeaderError('buffer too small for RIFF header');
  if (fourcc(view, 0) !== 'RIFF') throw new WavHeaderError('missing RIFF marker');
  if (fourcc(view, 8) !== 'WAVE') throw new WavHeaderError('missing WAVE marker');

  let offset = 12;
  let fmt: Omit<WavFormat, 'dataOffset' | 'dataBytes' | 'totalFrames'> | null = null;

  while (offset + 8 <= buf.byteLength) {
    const id = fourcc(view, offset);
    const size = view.getUint32(offset + 4, true);
    const body = offset + 8;

    if (id === 'fmt ') {
      if (size < 16 || body + 16 > buf.byteLength) throw new WavHeaderError('fmt chunk truncated');
      let tag = view.getUint16(body, true);
      const channels = view.getUint16(body + 2, true);
      const sampleRate = view.getUint32(body + 4, true);
      const blockAlign = view.getUint16(body + 12, true);
      const bitsPerSample = view.getUint16(body + 14, true);
      if (tag === FORMAT_EXTENSIBLE) {
        if (size < 40 || body + 26 > buf.byteLength) throw new WavHeaderError('extensible fmt chunk truncated');
        // SubFormat GUID: first two bytes carry the format tag.
        tag = view.getUint16(body + 24, true);
      }
      if (tag !== FORMAT_PCM && tag !== FORMAT_IEEE_FLOAT) {
        throw new WavHeaderError(`unsupported format tag ${tag}`);
      }
      if (channels < 1 || channels > 2) throw new WavHeaderError(`unsupported channel count ${channels}`);
      if (tag === FORMAT_IEEE_FLOAT && bitsPerSample !== 32) {
        throw new WavHeaderError(`unsupported float depth ${bitsPerSample}`);
      }
      if (tag === FORMAT_PCM && bitsPerSample !== 16 && bitsPerSample !== 24 && bitsPerSample !== 32) {
        throw new WavHeaderError(`unsupported PCM depth ${bitsPerSample}`);
      }
      if (blockAlign !== (channels * bitsPerSample) / 8) {
        throw new WavHeaderError(`blockAlign ${blockAlign} inconsistent with ${channels}ch x ${bitsPerSample}bit`);
      }
      if (sampleRate < 8000 || sampleRate > 384000) throw new WavHeaderError(`implausible sample rate ${sampleRate}`);
      fmt = {
        formatTag: tag,
        channels,
        sampleRate,
        bitsPerSample,
        blockAlign,
        isFloat: tag === FORMAT_IEEE_FLOAT,
      };
    } else if (id === 'data') {
      if (!fmt) throw new WavHeaderError('data chunk before fmt chunk');
      let dataBytes = size;
      if (dataBytes === 0xffffffff) {
        if (fileSize === undefined) throw new WavHeaderError('data size placeholder and unknown file size');
        dataBytes = fileSize - body;
      }
      if (fileSize !== undefined && body + dataBytes > fileSize) {
        dataBytes = Math.max(0, fileSize - body);
      }
      const totalFrames = Math.floor(dataBytes / fmt.blockAlign);
      return { ...fmt, dataOffset: body, dataBytes: totalFrames * fmt.blockAlign, totalFrames };
    }

    // Skip chunk body plus RIFF pad byte for odd sizes.
    offset = body + size + (size & 1);
  }
  throw new WavHeaderError(`no data chunk within the first ${buf.byteLength} bytes`);
}
