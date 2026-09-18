import type { WavFormat } from './wav-header';

/** Loop range in frames, half-open [start, end). */
export interface LoopRange {
  start: number;
  end: number;
}

export interface Chunk {
  /** Position in the linear stream (frames since flush). */
  streamStart: number;
  /** Position in the song (absolute frame in the file). */
  songStart: number;
  frames: number;
}

export const MIN_LOOP_FRAMES = 1024;

/**
 * Normalise a loop range: clamp to [0, totalFrames], reject empty or too
 * short ranges. Returns null when the range is unusable.
 */
export function validateLoop(
  loop: LoopRange | null | undefined,
  totalFrames: number,
  minFrames = MIN_LOOP_FRAMES,
): LoopRange | null {
  if (!loop) return null;
  const start = Math.max(0, Math.min(Math.floor(loop.start), totalFrames));
  const end = Math.max(0, Math.min(Math.floor(loop.end), totalFrames));
  if (!Number.isFinite(start) || !Number.isFinite(end)) return null;
  if (end - start < minFrames) return null;
  return { start, end };
}

/**
 * A play plan maps the linear stream the worklet consumes onto song positions.
 *
 * Starting at `startFrame`:
 *  - with a loop whose end is ahead of the start: play [start, loop.end) once
 *    (the prefix), then [loop.start, loop.end) forever (stream is infinite);
 *  - otherwise: play [start, totalFrames) once, then end of stream.
 *
 * A start position at or beyond loop.end plays through to the end, the usual
 * DAW convention.
 */
export class PlayPlan {
  readonly startFrame: number;
  readonly totalFrames: number;
  readonly loop: LoopRange | null;
  readonly prefixFrames: number;
  readonly loopFrames: number;
  /** Total stream length in frames; Infinity while looping. */
  readonly streamLength: number;

  constructor(startFrame: number, totalFrames: number, loop: LoopRange | null) {
    this.totalFrames = Math.max(0, Math.floor(totalFrames));
    this.startFrame = Math.max(0, Math.min(Math.floor(startFrame), this.totalFrames));
    const validLoop = validateLoop(loop, this.totalFrames);
    if (validLoop && this.startFrame < validLoop.end) {
      this.loop = validLoop;
      this.prefixFrames = validLoop.end - this.startFrame;
      this.loopFrames = validLoop.end - validLoop.start;
      this.streamLength = Infinity;
    } else {
      this.loop = null;
      this.prefixFrames = this.totalFrames - this.startFrame;
      this.loopFrames = 0;
      this.streamLength = this.prefixFrames;
    }
  }

  get isLooping(): boolean {
    return this.loop !== null;
  }

  /** Song frame corresponding to a linear stream frame. */
  songFrameAt(streamFrame: number): number {
    const s = Math.max(0, streamFrame);
    if (s < this.prefixFrames) return this.startFrame + s;
    if (this.loop) return this.loop.start + ((s - this.prefixFrames) % this.loopFrames);
    return this.totalFrames;
  }

  /**
   * Next contiguous chunk to fetch at `streamPos`, at most `maxFrames` long,
   * never crossing a segment boundary. Returns null at end of stream.
   */
  nextChunk(streamPos: number, maxFrames: number): Chunk | null {
    if (maxFrames <= 0) throw new Error('maxFrames must be positive');
    if (streamPos >= this.streamLength) return null;
    if (streamPos < this.prefixFrames) {
      const frames = Math.min(maxFrames, this.prefixFrames - streamPos);
      return { streamStart: streamPos, songStart: this.startFrame + streamPos, frames };
    }
    if (!this.loop) return null;
    const offset = (streamPos - this.prefixFrames) % this.loopFrames;
    const frames = Math.min(maxFrames, this.loopFrames - offset);
    return { streamStart: streamPos, songStart: this.loop.start + offset, frames };
  }
}

/** Inclusive byte range in the file for `frames` frames starting at `songStart`. */
export function byteRangeFor(
  fmt: Pick<WavFormat, 'dataOffset' | 'blockAlign' | 'totalFrames'>,
  songStart: number,
  frames: number,
): { start: number; end: number } {
  if (frames <= 0) throw new Error('frames must be positive');
  if (songStart < 0 || songStart + frames > fmt.totalFrames) {
    throw new Error(`frames ${songStart}..${songStart + frames} outside 0..${fmt.totalFrames}`);
  }
  const start = fmt.dataOffset + songStart * fmt.blockAlign;
  return { start, end: start + frames * fmt.blockAlign - 1 };
}

/**
 * Decide whether to issue the next fetch: there must be room for the whole
 * chunk in the ring and the buffered lead must be below the prefetch window.
 */
export function shouldFetch(buffered: number, space: number, chunkFrames: number, prefetchFrames: number): boolean {
  return space >= chunkFrames && buffered < prefetchFrames;
}
