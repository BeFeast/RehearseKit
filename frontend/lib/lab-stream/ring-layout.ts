/**
 * Shared memory layout for the streaming playback lab.
 *
 * One SharedArrayBuffer holds a control block (Int32) followed by planar
 * Float32 ring buffers, one per channel per stem. The layout object is the
 * single source of truth: the main thread and the AudioWorklet processor both
 * derive their typed-array views from it (the worklet receives it through
 * `processorOptions`), so nothing about the byte layout is hardcoded twice.
 *
 * Ownership of control words:
 *   main thread  -> STATE, EOF_POS, WRITE_POS[i]
 *   audio thread -> READ_POS, UNDERRUNS, ENDED, QUANTA
 *
 * READ_POS / WRITE_POS are absolute frame counters since the last flush (not
 * ring indices); the ring index is `pos & mask`. Buffered frames for stem i are
 * `(WRITE_POS[i] - READ_POS) | 0`.
 */

export const CTRL = {
  STATE: 0,
  READ_POS: 1,
  UNDERRUNS: 2,
  EOF_POS: 3,
  ENDED: 4,
  QUANTA: 5,
  WRITE_POS0: 8,
} as const;

export const STATE = {
  STOPPED: 0,
  PRIMING: 1,
  PLAYING: 2,
} as const;

export const CTRL_INT32S = 16;
export const CTRL_BYTES = CTRL_INT32S * 4;
export const MAX_STEMS = CTRL_INT32S - CTRL.WRITE_POS0;
export const MAX_CHANNELS = 2;

export interface StemLayout {
  channels: number;
  /** Byte offset inside the SharedArrayBuffer of each channel's Float32 ring. */
  channelByteOffsets: number[];
}

export interface RingLayout {
  ringFrames: number;
  mask: number;
  ctrlByteOffset: 0;
  stems: StemLayout[];
  totalBytes: number;
}

export function isPowerOfTwo(n: number): boolean {
  return Number.isInteger(n) && n > 0 && (n & (n - 1)) === 0;
}

export function createRingLayout(stemChannels: number[], ringFrames: number): RingLayout {
  if (!isPowerOfTwo(ringFrames)) {
    throw new Error(`ringFrames must be a power of two, got ${ringFrames}`);
  }
  if (stemChannels.length === 0 || stemChannels.length > MAX_STEMS) {
    throw new Error(`stem count must be 1..${MAX_STEMS}, got ${stemChannels.length}`);
  }
  let offset = CTRL_BYTES;
  const stems: StemLayout[] = stemChannels.map((channels) => {
    if (!Number.isInteger(channels) || channels < 1 || channels > MAX_CHANNELS) {
      throw new Error(`channels must be 1..${MAX_CHANNELS}, got ${channels}`);
    }
    const channelByteOffsets: number[] = [];
    for (let c = 0; c < channels; c++) {
      channelByteOffsets.push(offset);
      offset += ringFrames * 4;
    }
    return { channels, channelByteOffsets };
  });
  return { ringFrames, mask: ringFrames - 1, ctrlByteOffset: 0, stems, totalBytes: offset };
}
